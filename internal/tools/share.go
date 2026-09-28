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

// widgetArg reads share_with_room's optional structured `widget` argument.
// Absent (or null) is nil: the hand-off is words only. Present but not an
// object is an error, not a quiet fall back to text: an agent that sent a
// widget meant a widget, and used to learn otherwise only by its absence from
// the canvas. The payload itself is checked by ValidateWidget.
func widgetArg(args map[string]any) (*WidgetPayload, error) {
	v, ok := args["widget"]
	if !ok || v == nil {
		return nil, nil
	}
	raw, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("share_with_room: `widget` must be an object {widgetType, title, data} (got %s). Nothing was shared", shown(v))
	}
	data, _ := raw["data"].(map[string]any)
	return &WidgetPayload{
		WidgetType: str(raw, "widgetType"),
		Title:      str(raw, "title"),
		Data:       data,
	}, nil
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
		// A widget the room would refuse is refused HERE, in this result,
		// with what to fix. After "shared" the only way the agent hears about
		// it is a refusal line on a later call; before, it can fix and retry
		// now. The room still checks every widget itself.
		widget, err := widgetArg(args)
		if err != nil {
			return "", err
		}
		switch {
		case widget != nil:
			if problems := ValidateWidget(widget); len(problems) > 0 {
				return "", widgetProblem(widget, problems, "share", "shared")
			}
		case kind == "widget":
			return "", errors.New("share_with_room: kind \"widget\" needs `widget` with the values to plot, {widgetType, title, data}. " +
				"Nothing was shared. To share words only, use kind note or finding")
		}
		id, redacted, err := acc.Accept(cfg.IncidentID, cl.AgentInstanceID(), text, refsOf(args), widget, sourceQueryFailedOf(args), kind)
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
				return b.withRefusals(fmt.Sprintf("held — nothing left this machine. This names the person's working directory (%s), "+
					"and they have not allowed working-directory content into this room. Tell them: `landfall held` lists it, "+
					"`landfall allow-cwd` lets it through. Do not re-share it in other words.", strings.Join(matched, ", "))), nil
			}
		}
		// An expired room session means "the room will have this shortly"
		// would be false: nothing reaches the room until the person rejoins.
		// Said here, in this result, and counted as the agent being told.
		if waiting, expired := b.sessionExpired(cfg.IncidentID); expired {
			b.markExpiryTold(cfg.IncidentID)
			return b.withRefusals(expiredShareResult(waiting, redacted)), nil
		}
		if redacted {
			return b.withRefusals("shared — but something in it looked like a credential and was replaced with [redacted] " +
				"before it left this machine. Re-share without the secret if the room needs that detail."), nil
		}
		return b.withRefusals("shared — the room will have this shortly. Carry on; nothing to follow up."), nil
	}
}

// expiredShareResult is share_with_room's answer while the room session is
// expired: the share is kept on this machine, and what gets it to the room is
// the person rejoining, which only they can do.
func expiredShareResult(waiting int, redacted bool) string {
	var sb strings.Builder
	sb.WriteString("queued on this machine, not in the room yet: the room session has expired. ")
	if waiting > 1 {
		fmt.Fprintf(&sb, "It and the other %d waiting go out on their own once the person rejoins with a new link from the room. ", waiting-1)
	} else {
		sb.WriteString("It goes out on its own once the person rejoins with a new link from the room. ")
	}
	sb.WriteString("Tell them; do not share it again.")
	if redacted {
		sb.WriteString(" Something in it looked like a credential and was replaced with [redacted] before it was queued.")
	}
	return sb.String()
}

// Refusal is one earlier hand-off the room refused outright (a 4xx): what the
// agent shared, the room's own reason, and the HTTP status it came with.
type Refusal struct {
	Text   string
	Reason string
	// Status decides the advice: see correctable. Zero when unknown.
	Status int
}

// correctable reports whether a refusal names something in the hand-off the
// agent can fix: a malformed request (400), one too large (413) or one the
// room could not process as sent (422). A 403, a 404 or a closed engagement's
// 409 answers a corrected share exactly as it answered this one. An unknown
// status (zero) keeps the advice every refusal used to get.
func correctable(status int) bool {
	switch status {
	case 0, 400, 413, 422:
		return true
	}
	return false
}

// RefusalReporter is an OPTIONAL capability of an Accepter: the hand-offs the
// room refused that the agent has not been told about yet. Each is returned
// once. The spool-backed Accepter implements it; without it, a refused
// hand-off is still recorded, just never announced on a tool result.
type RefusalReporter interface {
	TakeRefusals(incidentID string) []Refusal
}

// refusalCap bounds how many refusals one tool result names individually.
const refusalCap = 5

// refusalsFor takes the room's untold refusals for the joined incident and
// renders them, or "" when there are none (or no queue to ask). Called on
// every room tool result, which is the only place an agent that was told
// "shared" can learn the room said no.
func (b *bridge) refusalsFor() string {
	rr, ok := b.acc.(RefusalReporter)
	if !ok {
		return ""
	}
	cl := b.sess.Client()
	if cl == nil {
		return ""
	}
	return RenderRefusals(rr.TakeRefusals(cl.Config().IncidentID))
}

// RenderRefusals is the agent-facing text for refused hand-offs: one short
// line each, naming the room's reason and enough of the text to tell which
// share it was, then what to do about them. "Share it again, corrected" is
// only said when the room named something the agent can correct; otherwise
// sharing again gets the same answer, and the person is the one to tell.
func RenderRefusals(rs []Refusal) string {
	if len(rs) == 0 {
		return ""
	}
	var sb strings.Builder
	fix, final := false, false
	for i, r := range rs {
		if correctable(r.Status) {
			fix = true
		} else {
			final = true
		}
		if i == refusalCap {
			fmt.Fprintf(&sb, "And %d more the room refused.\n", len(rs)-refusalCap)
			continue
		}
		if i > refusalCap {
			continue
		}
		reason := strings.TrimSpace(r.Reason)
		if reason == "" {
			reason = "the room refused it"
		}
		fmt.Fprintf(&sb, "Your earlier share did not reach the room: %s (it began %s).\n", reason, quote(snippet(r.Text, 60)))
	}
	if len(rs) == 1 {
		sb.WriteString("It will not be retried.")
	} else {
		sb.WriteString("None of these will be retried.")
	}
	switch {
	case fix && !final:
		sb.WriteString(" Share it again, corrected, if the room still needs it.")
	case final && !fix:
		sb.WriteString(" Sharing it again will get the same answer; tell the person.")
	default:
		sb.WriteString(" Where the room named something to fix, share it again, corrected, if the room still needs it. " +
			"Where it is closed or will not take it from this session, sharing again gets the same answer; tell the person.")
	}
	return sb.String()
}

// SessionReporter is an OPTIONAL capability of an Accepter: whether the room
// session that publishes this room's queue has expired (the room answered 401),
// and how many hand-offs wait on this machine for a new one. The spool-backed
// Accepter implements it.
type SessionReporter interface {
	SessionExpired(incidentID string) (waiting int, expired bool)
}

// sessionExpired asks the Accepter, when it can say.
func (b *bridge) sessionExpired(incidentID string) (int, bool) {
	sr, ok := b.acc.(SessionReporter)
	if !ok || incidentID == "" {
		return 0, false
	}
	return sr.SessionExpired(incidentID)
}

// markExpiryTold records that the agent has been told this room's session
// expired, so the notice is not repeated on every result while it stays so.
func (b *bridge) markExpiryTold(incidentID string) {
	b.mu.Lock()
	if b.expiryTold == nil {
		b.expiryTold = map[string]bool{}
	}
	b.expiryTold[incidentID] = true
	b.mu.Unlock()
}

// expiryNotice is the one-time line telling the agent the room session has
// expired, or "" when it has not (or the agent was already told). A session
// that is valid again clears the told mark, so a later expiry is told again.
func (b *bridge) expiryNotice() string {
	cl := b.sess.Client()
	if cl == nil {
		return ""
	}
	id := cl.Config().IncidentID
	waiting, expired := b.sessionExpired(id)
	b.mu.Lock()
	defer b.mu.Unlock()
	if !expired {
		delete(b.expiryTold, id)
		return ""
	}
	if b.expiryTold[id] {
		return ""
	}
	if b.expiryTold == nil {
		b.expiryTold = map[string]bool{}
	}
	b.expiryTold[id] = true
	shares := "Your shares are"
	if waiting == 1 {
		shares = "Your share is"
	}
	return fmt.Sprintf("The room session has expired. %s waiting on this machine (%d) and did not reach the room; "+
		"room tools will fail until the person rejoins with a new link from the room. Tell them. "+
		"What is waiting goes out on its own after that; do not share it again.", shares, waiting)
}

// snippet is the first n characters of text on one line, with an ellipsis
// when it was cut.
func snippet(text string, n int) string {
	one := strings.Join(strings.Fields(text), " ")
	runes := []rune(one)
	if len(runes) <= n {
		return one
	}
	return string(runes[:n]) + "…"
}

// withRefusals puts untold refusals, and a room session that has expired,
// ahead of a tool result: they are about the agent's own earlier work and ask
// something of it, the same reason vote requests go first.
func (b *bridge) withRefusals(result string) string {
	if told := b.refusalsFor(); told != "" {
		result = told + "\n\n" + result
	}
	if exp := b.expiryNotice(); exp != "" {
		result = exp + "\n\n" + result
	}
	return result
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
