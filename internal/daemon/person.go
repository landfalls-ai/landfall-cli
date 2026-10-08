package daemon

// person.go — the room as the PERSON reads it, beside the agent's view the
// rest of the daemon keeps (landfall-cli edge components, review findings 2,
// 5 and 7).
//
// Three reads go out with the room's session and NO agent instance id:
//
//   - GET …/vetting/attention: what awaits the person's vote, statements
//     included. The agent's own view (Seat.attention) drops what the agent has
//     already voted on and holds a teammate's gate-held claim back as a count.
//   - GET …/claims: where each claim stands, who wrote it, what it cites, and
//     what the bar asks for (each person's latest contribution, and the vote
//     list's needed and evidence).
//   - GET …/edge/lines: who holds which line. The ring holds fifty events, so
//     folding line.* rows from it would lose an old claim.
//
// WHEN THEY ARE READ. Never on the peek's own time: a hook peeks with a 250 ms
// budget and the status line every few seconds, so a peek answers from what is
// cached and reads in the background (the frame's own rule). A read is due
// when nothing has been read yet, when an event that changes it arrives
// (claim.* and context.* for the first two, line.* for the third), or when it
// is older than PersonTTL. That keeps a quiet room at three reads per room per
// half minute, far inside the edge's 60 requests per 10 seconds per address.

import (
	"context"
	"strings"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/narrate"
)

// PersonTTL is the longest a person read is served before it is read again
// with no event to prompt it.
const PersonTTL = 30 * time.Second

// personReadBudget bounds one background person read.
const personReadBudget = 10 * time.Second

// PersonReader is the room session's reads as the person. The real client has
// them; a test double that lacks them leaves the person's view empty.
type PersonReader interface {
	GetPersonAttention(ctx context.Context) (*client.Attention, error)
	GetClaims(ctx context.Context) (*client.ClaimsProjection, error)
	GetLines(ctx context.Context) ([]client.LineClaim, error)
}

// personState is what the daemon holds of the person's view of one room.
type personState struct {
	attention      *client.Attention
	attentionAt    time.Time
	attentionStale bool

	claims      *client.ClaimsProjection
	claimsAt    time.Time
	claimsStale bool

	lines      []client.LineClaim
	linesAt    time.Time
	linesStale bool

	loading bool
	// conclusion is the latest concluded run's answer, kept after the events
	// that said it leave the ring.
	conclusion *narrate.BeaconConclusion
}

// markPersonLocked records what an arriving event changes in the person's
// view. Called with r.mu held.
func (r *Room) markPersonLocked(evt client.Event) {
	p := &r.person
	if narrate.TouchesAttention(evt.Type) {
		p.attentionStale, p.claimsStale = true, true
	}
	if strings.HasPrefix(evt.Type, "line.") {
		p.linesStale = true
	}
	if evt.Type == "agent.run.concluded" {
		r.keepConclusionLocked()
	}
}

// keepConclusionLocked keeps the newest concluded run the ring can explain.
func (r *Room) keepConclusionLocked() {
	c := narrate.BeaconConclusionOf(r.events)
	if c != nil && (r.person.conclusion == nil || c.Seq >= r.person.conclusion.Seq) {
		r.person.conclusion = c
	}
}

// refreshPersonAsync reads, in the background, whichever person reads are due.
func (r *Room) refreshPersonAsync() {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := &r.person
	if p.loading || r.client == nil {
		return
	}
	pr, ok := r.client.(PersonReader)
	if !ok {
		return
	}
	now := r.deps.now()
	due := func(have bool, at time.Time, stale bool) bool {
		return !have || stale || now.Sub(at) >= PersonTTL
	}
	wantAtt := due(!p.attentionAt.IsZero(), p.attentionAt, p.attentionStale)
	wantClaims := due(!p.claimsAt.IsZero(), p.claimsAt, p.claimsStale)
	wantLines := due(!p.linesAt.IsZero(), p.linesAt, p.linesStale)
	if !wantAtt && !wantClaims && !wantLines {
		return
	}
	// Cleared now, not when the read lands: an event arriving during the read
	// marks it again, and the next peek reads once more.
	if wantAtt {
		p.attentionStale = false
	}
	if wantClaims {
		p.claimsStale = false
	}
	if wantLines {
		p.linesStale = false
	}
	p.loading = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), personReadBudget)
		defer cancel()
		var (
			att                         *client.Attention
			claims                      *client.ClaimsProjection
			lines                       []client.LineClaim
			attErr, claimsErr, linesErr error
		)
		if wantAtt {
			att, attErr = pr.GetPersonAttention(ctx)
		}
		if wantClaims {
			claims, claimsErr = pr.GetClaims(ctx)
		}
		if wantLines {
			lines, linesErr = pr.GetLines(ctx)
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		at := r.deps.now()
		// A failed read keeps what was there and waits out PersonTTL before
		// asking again: a server without the route must not be asked on
		// every peek.
		if wantAtt {
			if attErr == nil {
				p.attention = att
			}
			p.attentionAt = at
		}
		if wantClaims {
			if claimsErr == nil {
				p.claims = claims
			}
			p.claimsAt = at
		}
		if wantLines {
			if linesErr == nil {
				p.lines = lines
			}
			p.linesAt = at
		}
		p.loading = false
	}()
}

// personLoading reports a person read in flight.
func (r *Room) personLoading() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.person.loading
}

// VotesView is what awaits the PERSON's position, from cache (never a network
// wait): the human attention view, enriched from the claims projection.
func (r *Room) VotesView() []narrate.Vote {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.person
	if p.attention == nil {
		return nil
	}
	var parts []client.Participant
	if r.frame != nil {
		parts = r.frame.Participants
	}
	elapsed := float64(r.deps.now().Sub(p.attentionAt).Milliseconds())
	if elapsed < 0 {
		elapsed = 0
	}
	return narrate.VotesOf(p.attention, p.claims, parts, r.Config.HumanActorID, elapsed)
}
