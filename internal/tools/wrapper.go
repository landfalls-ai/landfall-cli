package tools

// wrapper.go — the Go equivalent of `narrated()` in `src/tools.mjs` (lines
// 512–544). Every tool call heartbeats a "doing" + posts a contribution if the
// action produced a durable artifact — and carries back whatever other
// investigators published while the agent was busy, which is the only way that
// context reaches an agent that never chose to call get_updates.

import (
	"context"
	"errors"

	"github.com/landfalls-ai/landfall-cli/internal/mcp"
	"github.com/landfalls-ai/landfall-cli/internal/narrate"
	"github.com/landfalls-ai/landfall-cli/internal/session"
)

// errNotConnected is the exact text the Node original throws, surfaced to the
// caller through `tools/call`'s isError:true result path — never as a JSON-RPC
// error.
var errNotConnected = errors.New("not connected — call join_war_room with a Landfall agent share link first")

// bridge holds the session the 16 tools are built over.
type bridge struct {
	sess *session.Session
}

// handlerFunc is a tool's own work, run between the narration prologue and the
// piggyback epilogue.
type handlerFunc func(ctx context.Context, args map[string]any, doing string, cl session.EdgeClient) (string, error)

// requireClient fails closed with a helpful error before anything else in the
// wrapper runs — including before the heartbeat.
func (b *bridge) requireClient() (session.EdgeClient, error) {
	cl := b.sess.Client()
	if cl == nil {
		return nil, errNotConnected
	}
	return cl, nil
}

// narrated wraps a handler in the nine-step per-call order. The order is
// load-bearing and verified against the source; see the numbered comments.
func (b *bridge) narrated(name string, run handlerFunc) mcp.Handler {
	return func(ctx context.Context, args map[string]any) (string, error) {
		// 0. Fail closed before the heartbeat, so an unjoined session never
		//    narrates anything anywhere.
		cl, err := b.requireClient()
		if err != nil {
			return "", err
		}

		// 1. Presence heartbeat — best-effort; narration never fails a call.
		doing := narrate.NarrateDoing(name, args)
		_ = cl.Heartbeat(ctx, doing)

		// 2. A durable contribution, when this action produced one — also
		//    best-effort.
		contrib := narrate.ContributionFor(name, args)
		if contrib != nil {
			_ = cl.Contribute(ctx, contrib.Kind, contrib.Fields())

			// 3. A `query`-kind contribution IS this session's own new
			//    `edge.query` — the other half of what divergence depends on
			//    (the room's established direction is the other half, dirtied
			//    when a claim/context event arrives).
			if contrib.Kind == "query" {
				b.sess.MarkDivergenceDirty()
				b.sess.RefreshDivergenceAsync(ctx)
			}
		}

		// 4. The tool's own work.
		result, err := run(ctx, args, doing, cl)
		if err != nil {
			return "", err
		}

		// 5. Flush AFTER the handler: a pull the agent made itself has already
		//    advanced the cursor past what it delivered, so the block only ever
		//    carries what the socket pushed beyond that — never a duplicate of
		//    the result above it.
		flushed := b.flushPending(ctx)

		// 6. Vote requests ride the same channel and are PREPENDED: they are
		//    the one thing here addressed TO the agent rather than reporting
		//    what happened, and burying an "answer this" under a tool result
		//    and a digest is how it gets skimmed past.
		asked := b.flushVoteRequests(ctx)

		// 7. A divergence nudge is the SAME tier but a SOFTER one (an
		//    invitation to reconsider, not "answer this"), so it is ordered
		//    after a pending vote request when both fire on the same result.
		nudge := b.flushDivergenceNudge()

		// 8. Compose: asked + nudge + result + delta.
		body := result
		if flushed != "" {
			body = result + "\n\n" + flushed
		}
		withNudge := body
		if nudge != "" {
			withNudge = nudge + "\n\n" + body
		}
		if asked != "" {
			return asked + "\n\n" + withNudge, nil
		}
		return withNudge, nil
	}
}

// flushPending drains what is owed onto a tool result. The local queue is the
// TRIGGER only; the CONTENT is always the server's own classified delta, never
// the raw locally-queued events.
func (b *bridge) flushPending(ctx context.Context) string {
	delta := b.sess.FlushPending(ctx)
	if delta == nil {
		return ""
	}
	return narrate.RenderDelta(delta)
}

// flushVoteRequests drains new vote requests onto a tool result.
//
// It FORCE-refreshes the projection when a room event said it could have
// changed (or it has never been read), so a quiet room costs ZERO extra
// requests per tool call — the cost of this channel scales with what is
// happening in the war room, not with how busy the agent is.
//
// A claim is announced ONCE, and once more if it later goes stale. The keys
// are marked only after the block is in hand, so a failed read cannot silently
// consume the announcement. Never fails: a failed read leaves the result
// untouched.
func (b *bridge) flushVoteRequests(ctx context.Context) string {
	att := b.sess.RefreshAttentionIfStale(ctx)
	block := narrate.VoteRequestBlock(att, b.sess.AttentionNotified(), 0)
	b.sess.MarkAttentionNotified(block.Keys)
	return block.Text
}

// flushDivergenceNudge drains a new divergence nudge onto a tool result.
//
// Deliberately does NOT force a refresh the way flushVoteRequests does:
// divergence is background-refreshed by its own two triggers (a claim/context
// arrival, and this wrapper's own query-contribution path) and this just reads
// whatever landed. Forcing a synchronous fetch on every tool call would add
// real latency to the fast path. Do not "make it consistent" with step 6.
func (b *bridge) flushDivergenceNudge() string {
	block := narrate.DivergenceBlock(b.sess.Divergence(), b.sess.DivergenceNotified())
	b.sess.MarkDivergenceNotified(block.Keys)
	return block.Text
}
