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
//
// AN EVENT STARTS THE READ ITSELF (live.md FR-L1). A claim.* or line.* event
// used to mark the view stale and wait for somebody to peek: a watch woken by
// that same event peeked before the mark (the subscribers were told first),
// found nothing to read, and the read waited for a later peek. The event now
// marks the view and starts the read before anyone is woken, and when the
// read lands with a different answer every watch is woken again
// (PersonChanged), so a vote is a band line one round trip after its event.
//
// A TEAMMATE'S STAGED CLAIM IS NEVER PUSHED HERE. The server withholds every
// claim the gate has not admitted from edge sockets (core-api
// realtime-vetting.filter.ts: an ownerless edge viewer sees admitted context
// only), and this daemon's socket is an edge socket. So the vote it asks the
// person for arrives with no event at all, and waiting for PersonTTL made
// the vote card up to 30 s late (measured live: 10.3 s). While a watch is
// attached (somebody is looking at the band), the person's attention alone
// is read every PersonWatchedPoll, and the claims with it only when that read
// names a claim the claims projection does not hold yet: one request per
// 1.5 s per watched room, about 7 of the edge's 60 per 10 s.
//
// BOUNDED. One person read is in flight per room at a time, and event reads
// start at least personMinGap apart (a burst of claim events is one read now
// and one more a second later, never a storm): at most three requests a
// second per room, plus the watched poll's one per 1.5 s, and only during a
// burst. The poll neither waits for the gap nor moves it, so a claim event
// just after a poll read is still read at once.

import (
	"context"
	"encoding/json"
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

// PersonWatchedPoll is how often the person's attention is read while a watch
// is attached, for the votes no event announces (see above).
const PersonWatchedPoll = 1500 * time.Millisecond

// personMinGap is the least time between the starts of two person reads.
const personMinGap = time.Second

// PersonChanged is the type of the seq-less line SubscribeAll sends when a
// person read lands with a different answer (a vote appeared or went, a
// claim moved, a line changed hands).
const PersonChanged = "landfall.person.changed"

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
	// scheduled is a read waiting out personMinGap; lastStart is when the
	// last read started, on the wall clock (pacing is about real time even
	// when a test drives the room's clock).
	scheduled bool
	lastStart time.Time
	// sig is the last landed answer, to tell a changed one from a re-read.
	sig string
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
	r.refreshPersonLocked()
}

// refreshPersonLocked starts the due person reads, paced by personMinGap.
func (r *Room) refreshPersonLocked() { r.startPersonReadLocked(false) }

// startPersonReadLocked starts the due reads (or schedules them, inside
// personMinGap) and returns at once; r.mu is held. A poll read (the watched
// poll's own beat, already one per PersonWatchedPoll) is not paced and does
// not pace: a claim event right after a poll read is read at once.
func (r *Room) startPersonReadLocked(poll bool) {
	p := &r.person
	if p.loading || p.scheduled || r.client == nil {
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
	if wait := personMinGap - time.Since(p.lastStart); !poll && !p.lastStart.IsZero() && wait > 0 {
		p.scheduled = true
		time.AfterFunc(wait, func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.person.scheduled = false
			r.refreshPersonLocked()
		})
		return
	}
	// Cleared now, not when the read lands: an event arriving during the read
	// marks it again, and it is read once more when this one lands.
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
	if !poll {
		p.lastStart = time.Now()
	}
	held := p.claims
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
			// A vote the claims projection has not heard of (a teammate's
			// claim found by the watched poll, with no event): read the
			// claims too, for its bar and evidence.
			if attErr == nil && !wantClaims && namesUnheldClaim(att, held) {
				wantClaims = true
			}
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
		if sig := personSig(p); sig != p.sig {
			p.sig = sig
			r.wakeAllTypeLocked(PersonChanged)
		}
		// An event that arrived during the read marked the view again.
		if p.attentionStale || p.claimsStale || p.linesStale {
			r.refreshPersonLocked()
		}
	}()
}

// pollPersonLocked is the watched poll's beat: with a watch attached, the
// person's attention is due now. Called with r.mu held.
func (r *Room) pollPersonLocked() {
	if len(r.wakers) == 0 {
		return
	}
	r.person.attentionStale = true
	r.startPersonReadLocked(true)
}

// namesUnheldClaim reports a vote in att whose claim the projection lacks.
func namesUnheldClaim(att *client.Attention, claims *client.ClaimsProjection) bool {
	if att == nil {
		return false
	}
	held := map[int64]bool{}
	if claims != nil {
		for _, c := range claims.Claims {
			held[c.Seq] = true
		}
	}
	for _, v := range att.VotesAwaited {
		if v.ClaimSeq != nil && !held[*v.ClaimSeq] {
			return true
		}
	}
	return false
}

// personSig is the person's view as one string, to tell a changed answer from
// the same one read again. expiresInMs counts down on every read, so it is
// left out: a vote's clock moving is not news.
func personSig(p *personState) string {
	type vote struct {
		Seq       int64
		Positions int
		Stale     bool
		Statement string
	}
	var votes []vote
	if p.attention != nil {
		for _, v := range p.attention.VotesAwaited {
			if v.ClaimSeq != nil {
				votes = append(votes, vote{*v.ClaimSeq, v.PositionsSoFar, v.Stale, v.Statement})
			}
		}
	}
	body, _ := json.Marshal(struct {
		Votes  []vote
		Claims *client.ClaimsProjection
		Lines  []client.LineClaim
	}{votes, p.claims, p.lines})
	return string(body)
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
