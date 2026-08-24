package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/landfalls-ai/landfall-cli/internal/mcp"
)

// Accepter is the durable outbound queue, as this package needs it. An
// interface so internal/tools does not import internal/spool — the layering
// rule is that tools depends on session/client/mcp/narrate and nothing heavier.
type Accepter interface {
	// Accept durably records a hand-off. It must not perform network I/O.
	//
	// redacted reports that the text was altered on the way in (FR-010). The
	// caller is expected to SAY so: quietly rewriting what someone wrote means
	// they find out later, from the timeline, which is worse.
	Accept(incidentID, agentInstanceID, text string, refs []string) (id string, redacted bool, err error)
}

// ErrQueueFull is what an Accepter reports when the outbound queue is at its
// bound. Kept as a sentinel here so share_with_room can say something true and
// specific rather than a generic failure.
var ErrQueueFull = errors.New("outbound queue is full")

// shareWithRoom is the single fire-and-forget publish verb (spec FR-001).
//
// REGISTERED UN-WRAPPED, deliberately. Every other publishing tool goes
// through b.narrated(), which heartbeats and may post a durable contribution
// BEFORE the handler runs (wrapper.go:53-71) — network work on the calling
// path. This tool must return without any of that, so it registers a bare
// handler exactly as join_war_room already does (tools.go:112-116). No wrapper
// surgery was needed; the un-wrapped pattern was already in the codebase.
//
// It also carries nothing back (FR-002b). The wrapper's three piggyback tiers
// stay enabled for other tools — D8 keeps the channels open and narrows what
// is in the queue instead — but a hand-off is not a delivery vehicle, so this
// result is one line and nothing else.
func (b *bridge) shareWithRoom(acc Accepter) mcp.Handler {
	return func(_ context.Context, args map[string]any) (string, error) {
		text, _ := args["text"].(string)
		if text == "" {
			return "", errors.New("share_with_room needs `text` — what you found, in your own words")
		}

		cl := b.sess.Client()
		if cl == nil {
			// Fail closed, and say what to do about it. Silently spooling for a
			// room we have not joined would strand the finding somewhere the
			// responder never looks.
			return "", errors.New("not joined to a war room yet — call join_war_room first")
		}

		cfg := cl.Config()
		id, redacted, err := acc.Accept(cfg.IncidentID, cl.AgentInstanceID(), text, refsOf(args))
		switch {
		case errors.Is(err, ErrQueueFull):
			// FR-012: never silent. A responder must not believe a finding
			// reached the room when it did not.
			return "", fmt.Errorf("the outbound queue is full, so this was NOT recorded — " +
				"the room is unreachable or falling behind; try again shortly")
		case err != nil:
			return "", fmt.Errorf("could not record the hand-off: %w", err)
		}

		_ = id
		if redacted {
			return "shared — but something in it looked like a credential and was replaced with [redacted] " +
				"before it left this machine. Re-share without the secret if the room needs that detail.", nil
		}
		return "shared — the room will have this shortly. Carry on; nothing to follow up.", nil
	}
}

// refsOf pulls the optional local references out of the argument map,
// tolerating both []string and the []any that JSON decoding produces.
func refsOf(args map[string]any) []string {
	raw, ok := args["refs"]
	if !ok || raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}
