package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/landfalls-ai/landfall-cli/internal/mcp"
)

// Accepter is the durable outbound queue, as this package needs it. An
// interface so internal/tools does not import internal/spool — the layering
// rule is that tools depends on session/client/mcp/narrate and nothing heavier.
type Accepter interface {
	// Accept durably records a hand-off. It must not perform network I/O.
	//
	// widget carries the structured widgetType/title/data an edge agent
	// computed itself, when it supplied one — nil for every hand-off that
	// isn't a widget, or that is one described only by a free-text marker
	// (see the CLI's classify.go on why a marker alone yields an empty tile).
	//
	// redacted reports that the text was altered on the way in (FR-010). The
	// caller is expected to SAY so: quietly rewriting what someone wrote means
	// they find out later, from the timeline, which is worse.
	//
	// sourceQueryFailed is share_with_room's caller's OWN self-report that
	// this hand-off was produced after one of its own tool calls failed —
	// never independently verified here, carried through so the room's
	// admission gate can screen it (Landfall feature 20260920-132909).
	Accept(incidentID, agentInstanceID, text string, refs []string, widget *WidgetPayload, sourceQueryFailed bool, kind string) (id string, redacted bool, err error)
}

// WidgetPayload is the structured content of a widget hand-off, as
// share_with_room's optional `widget` argument carries it. Mirrored (not
// shared) in internal/spool.WidgetPayload — the layering rule above means
// this package cannot import that one, so internal/bridge's Accepter
// translates between the two at the boundary, the same way it already
// translates spool.ErrFull into ErrQueueFull.
type WidgetPayload struct {
	WidgetType string
	Title      string
	Data       map[string]any
}

// widgetOf reads share_with_room's optional structured `widget` argument.
// Absent or malformed returns nil — the hand-off still queues as plain text
// and Classify falls back to its marker heuristic, so a bad `widget` value
// degrades rather than fails the whole call.
func widgetOf(args map[string]any) *WidgetPayload {
	raw, ok := args["widget"].(map[string]any)
	if !ok {
		return nil
	}
	data, _ := raw["data"].(map[string]any)
	return &WidgetPayload{
		WidgetType: str(raw, "widgetType"),
		Title:      str(raw, "title"),
		Data:       data,
	}
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
		kind, err := kindOf(args)
		if err != nil {
			return "", err
		}
		id, redacted, err := acc.Accept(cfg.IncidentID, cl.AgentInstanceID(), text, refsOf(args), widgetOf(args), sourceQueryFailedOf(args), kind)
		switch {
		case errors.Is(err, ErrQueueFull):
			// FR-012: never silent. A responder must not believe a finding
			// reached the room when it did not.
			return "", fmt.Errorf("the outbound queue is full, so this was NOT recorded — " +
				"the room is unreachable or falling behind; try again shortly")
		case err != nil:
			return "", fmt.Errorf("could not record the hand-off: %w", err)
		}

		if hr, ok := acc.(HeldReporter); ok {
			if matched := hr.HeldReason(id); len(matched) > 0 {
				return fmt.Sprintf("held — nothing left this machine. This names the person's working directory (%s), "+
					"and they have not allowed working-directory content into this room. Tell them: `landfall held` lists it, "+
					"`landfall allow-cwd` lets it through. Do not re-share it in other words.", strings.Join(matched, ", ")), nil
			}
		}
		if redacted {
			return "shared — but something in it looked like a credential and was replaced with [redacted] " +
				"before it left this machine. Re-share without the secret if the room needs that detail.", nil
		}
		return "shared — the room will have this shortly. Carry on; nothing to follow up.", nil
	}
}

// HeldReporter is an OPTIONAL capability of an Accepter: after Accept, it can
// say whether that hand-off was held by the working-directory rule and what
// matched. The daemon-mode front end implements it; a plain spool does not,
// and the handler then reports "shared" exactly as before.
type HeldReporter interface {
	HeldReason(id string) []string
}

// ShareKinds is what `kind` may name. Chat with the room is a note; a finding
// and a claim enter the admission gate; a widget needs `widget`.
var ShareKinds = []string{"note", "finding", "claim", "widget"}

// kindOf reads the optional explicit publish kind. Absent is "" (the worker
// classifies from the text); anything else must be one of ShareKinds.
func kindOf(args map[string]any) (string, error) {
	v, ok := args["kind"]
	if !ok || v == nil {
		return "", nil
	}
	k, _ := v.(string)
	for _, allowed := range ShareKinds {
		if k == allowed {
			return k, nil
		}
	}
	return "", fmt.Errorf("share_with_room: kind must be one of %s, got %q", strings.Join(ShareKinds, ", "), k)
}

// sourceQueryFailedOf reads the optional self-report that this hand-off
// followed a failed tool call (Landfall feature 20260920-132909). Absent or
// not literally `true` is `false` — only an explicit, affirmative self-report
// counts, the same way `widgetOf` requires an explicit structured value
// rather than inferring one.
func sourceQueryFailedOf(args map[string]any) bool {
	v, _ := args["sourceQueryFailed"].(bool)
	return v
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
