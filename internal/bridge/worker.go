// Package bridge is the background war-room worker that lives inside
// `landfall serve`.
//
// WHAT IT IS FOR (spec D8). It is a CONSUMER that keeps the room queue
// drained — not a wall that stops it draining. That distinction is the whole
// feature, and getting it backwards is what the first design did.
//
// The original plan suppressed the in-band delivery tiers so nothing reached
// the agent mid-turn. The independent design review showed that backfires:
// every tier drains the same queue, and internal/hooks/userpromptsubmit.go:25-30
// says in its own words that the wrapper's in-band drain after every tool call
// is WHY the prompt and Stop tiers stay quiet. Suppress the drain and the queue
// fills, so the prompt tier fires on nearly every prompt and Stop blocks nearly
// every conclusion. The tool results would look clean and the responder would
// be interrupted more.
//
// So the worker is deliberately EAGER. Per-item disposition, using the
// classification the server already computes
// (three classes, computed server-side per viewer):
//
//	routine      already dropped server-side. Nothing to do.
//	substantive  THE WORKER'S. Absorb it so it never waits for flushPending to
//	             render it into someone's tool result.
//	addressed    THE AGENT'S, in band, unchanged. A vote awaited on the
//	             responder's own claim is exactly what they need to see;
//	             suppressing it would be a defect, not the feature.
//
// LIFECYCLE mirrors liveSession (internal/cli/serve.go:118-175) because it has
// the same hazard: join_war_room can re-target the session mid-process, so
// start() must stop() first, and per-room state must reset with the session's
// own reset (internal/session/session.go's JoinWarRoom clears cursor/pending).
//
// FR-011: stderr only. serve's stdout is the MCP wire (internal/cli/serve.go:16-21)
// and one stray write corrupts the JSON-RPC stream.
package bridge

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/mirror"
	"github.com/landfalls-ai/landfall-cli/internal/spool"
)

// DrainInterval is how often the worker sweeps when nothing has nudged it.
//
// This is a FLOOR on responsiveness, not the primary mechanism: live push
// wakes the worker as events arrive (v0.6.2 wired internal/realtime into
// serve). The sweep exists because push is best-effort by design — a socket
// that never comes up logs one line and changes nothing else — and because of
// FR-002c: an IDLE agent still needs its queue drained, or the prompt-submit
// and Stop tiers silently become the delivery path.
const DrainInterval = 5 * time.Second

// Publisher is the slice of the room API the worker writes through. An
// interface so the drain loop is testable without a server.
type Publisher interface {
	// Contribute publishes one classified item to the room.
	Contribute(ctx context.Context, kind string, body map[string]any) error
	// GetUpdates returns raw timeline events since a cursor. Raw, not the
	// classified delta: the delta drops our own events, which is precisely
	// what reconciliation must find.
	GetUpdates(ctx context.Context, sinceSeq int64) ([]client.Event, error)
	// AgentInstanceID is this session's server-issued id, empty before join.
	AgentInstanceID() string
}

// Logger receives the worker's one-line notices. Always stderr (FR-011).
type Logger func(format string, args ...any)

// Worker is the background bridge worker for one `serve` process.
type Worker struct {
	spool  *spool.Spool
	mirror *mirror.Mirror
	log    Logger

	// Interval overrides DrainInterval. Zero means DrainInterval.
	//
	// Exported because a hardcoded cadence is an untestable one: FR-002c
	// ("the queue drains while the agent is idle") can only be proven by
	// watching a sweep happen on its own, and a test should not have to wait
	// out a production interval to see it.
	Interval time.Duration

	mu     sync.Mutex
	cancel context.CancelFunc
	nudge  chan struct{}
}

func (w *Worker) interval() time.Duration {
	if w.Interval > 0 {
		return w.Interval
	}
	return DrainInterval
}

// New builds a Worker. It does not start; call Start on join.
func New(sp *spool.Spool, mi *mirror.Mirror, log Logger) *Worker {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &Worker{spool: sp, mirror: mi, log: log, nudge: make(chan struct{}, 1)}
}

// Start begins (or restarts) the worker for a freshly joined room.
//
// Stops any previous run first, exactly as liveSession.start does
// (serve.go:131): a join can arrive on an MCP handler goroutine while shutdown
// runs on another, and a worker still holding the previous room's state would
// publish a finding into a room the responder has left.
func (w *Worker) Start(ctx context.Context, pub Publisher, cfg client.Config) {
	w.Stop()

	runCtx, cancel := context.WithCancel(ctx)
	w.mu.Lock()
	w.cancel = cancel
	w.mu.Unlock()

	go w.run(runCtx, pub, cfg)
}

// Stop ends the worker. Idempotent — shutdown calls it, and so does the next
// join.
func (w *Worker) Stop() {
	w.mu.Lock()
	cancel := w.cancel
	w.cancel = nil
	w.mu.Unlock()

	if cancel != nil {
		cancel()
	}
}

// Nudge asks the worker to sweep now. Called when live push delivers an event,
// so the common case does not wait out DrainInterval. Never blocks: a nudge
// that cannot be delivered means one is already pending, which is the same
// outcome.
func (w *Worker) Nudge() {
	select {
	case w.nudge <- struct{}{}:
	default:
	}
}

func (w *Worker) run(ctx context.Context, pub Publisher, cfg client.Config) {
	// Reconcile BEFORE draining anything. Entries left Publishing by a dead
	// process have unknown outcomes, and publishing the queue first would
	// duplicate whichever of them already landed.
	w.reconcile(ctx, pub, cfg.IncidentID)

	ticker := time.NewTicker(w.interval())
	defer ticker.Stop()

	for {
		w.sweep(ctx, pub, cfg.IncidentID)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-w.nudge:
		}
	}
}

// sweep does one pass: pull what the room has, absorb it, then publish what we
// owe. Order matters — recording first means a publish that lands during this
// pass is already reconcilable if we crash immediately after.
func (w *Worker) sweep(ctx context.Context, pub Publisher, incidentID string) {
	w.absorb(ctx, pub, incidentID)
	w.drain(ctx, pub, incidentID)
}

// absorb pulls raw timeline events into the mirror (D8 + D9).
//
// Raw, not the classified delta: the delta is the right feed for deciding what
// deserves the agent's attention, but it drops our own events, and those are
// what reconciliation needs.
func (w *Worker) absorb(ctx context.Context, pub Publisher, incidentID string) {
	since, err := w.mirror.LastSeq(incidentID)
	if err != nil {
		w.log("bridge: mirror unreadable (%v) — skipping this sweep", err)
		return
	}

	events, err := pub.GetUpdates(ctx, since)
	if err != nil {
		// Best-effort by design: a room we cannot reach right now is not an
		// error the responder needs to see mid-investigation.
		return
	}
	if len(events) == 0 {
		return
	}
	if err := w.mirror.Record(incidentID, events); err != nil {
		w.log("bridge: could not record %d events (%v)", len(events), err)
	}
}

// drain publishes queued hand-offs, oldest first.
func (w *Worker) drain(ctx context.Context, pub Publisher, incidentID string) {
	for {
		if ctx.Err() != nil {
			return
		}
		entry, err := w.spool.Next(incidentID)
		if err != nil {
			w.log("bridge: spool unreadable (%v)", err)
			return
		}
		if entry == nil {
			return
		}
		if !w.publish(ctx, pub, entry) {
			// Stop the pass on the first failure rather than hammering a room
			// that is refusing everything; the next sweep retries with the
			// entry's raised attempt count.
			return
		}
	}
}

// publish sends one entry and records the outcome. Returns false when the pass
// should stop.
func (w *Worker) publish(ctx context.Context, pub Publisher, e *spool.Entry) bool {
	// Claim BEFORE the network call. A crash after this point is recognizable
	// afterwards as "outcome unknown" rather than looking like it never ran —
	// which is what lets reconciliation avoid a duplicate.
	if err := w.spool.Claim(e.IncidentID, e.ID); err != nil {
		w.log("bridge: could not claim %s (%v)", e.ID, err)
		return false
	}

	kind := Classify(e.Text)
	body := map[string]any{
		"text": e.Text,
		// FR-004: provenance marks this as bridge-published rather than a
		// direct action by the responder. It is a property of the ITEM, under
		// the responder's single room identity — spec D1. Minting a second
		// identity would let one human corroborate their own claim, silently
		// inflating vetting quorum, which is why this is a field and not an
		// actor.
		"provenance": "bridge",
	}
	if len(e.Refs) > 0 {
		body["refs"] = e.Refs
	}
	if err := pub.Contribute(ctx, string(kind), body); err != nil {
		if ferr := w.spool.Fail(e.IncidentID, e.ID, err); ferr != nil {
			w.log("bridge: could not record failure for %s (%v)", e.ID, ferr)
		}
		return false
	}

	if err := w.spool.Ack(e.IncidentID, e.ID); err != nil {
		// The room HAS it; we simply failed to write that down. Reconciliation
		// on the next start will find it and ack then, so this is a log line
		// rather than a republish.
		w.log("bridge: published %s but could not ack it locally (%v)", e.ID, err)
	}
	return true
}

// reconcile resolves entries whose outcome was lost to a crash (D9).
//
// For each, the mirror is asked whether a matching event of ours already
// landed. Found means ack — republishing would duplicate a finding in the
// room. Not found means requeue. Guessing either way costs the responder
// something they cannot get back.
func (w *Worker) reconcile(ctx context.Context, pub Publisher, incidentID string) {
	unknown, err := w.spool.Recover(incidentID)
	if err != nil {
		w.log("bridge: could not read the spool for reconciliation (%v)", err)
		return
	}
	if len(unknown) == 0 {
		return
	}

	// Refresh the mirror first: the events we need to recognize may have
	// landed in the window we were dead for.
	w.absorb(ctx, pub, incidentID)

	acked, requeued := 0, 0
	for _, e := range unknown {
		landed, err := w.mirror.FindOwn(incidentID, e.AgentInstanceID, e.Text, 0)
		if err != nil {
			w.log("bridge: reconciliation failed for %s (%v)", e.ID, err)
			continue
		}
		if landed {
			if err := w.spool.Ack(incidentID, e.ID); err != nil {
				w.log("bridge: could not ack reconciled %s (%v)", e.ID, err)
				continue
			}
			acked++
			continue
		}
		if err := w.spool.Fail(incidentID, e.ID, errors.New("outcome unknown after restart; republishing")); err != nil {
			w.log("bridge: could not requeue %s (%v)", e.ID, err)
			continue
		}
		requeued++
	}
	if acked > 0 || requeued > 0 {
		w.log("bridge: reconciled %d hand-off(s) after restart — %d already in the room, %d to republish", acked+requeued, acked, requeued)
	}
}

// Abandon drops hand-offs for a room the responder has left, and says how
// many. Silence here would let a responder believe findings reached a room
// they never will.
func (w *Worker) Abandon(incidentID string) {
	n, err := w.spool.Abandon(incidentID)
	if err != nil {
		w.log("bridge: could not abandon queued hand-offs for %s (%v)", incidentID, err)
		return
	}
	if n > 0 {
		w.log("bridge: %d unsent hand-off(s) were dropped when you left incident %s", n, incidentID)
	}
}
