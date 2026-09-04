// Package narrate turns a local agent's tool call into a human "doing" line
// (and, where it carries a durable artifact, a timeline contribution) — and
// owns the reverse direction, rendering a shared room event published by
// someone else into one readable line.
//
// Ported from src/narrate.mjs, src/attention.mjs and src/context-render.mjs
// (all three pure, no I/O, no clock, no module state in the Node source —
// this package keeps that discipline: no time.Now(), no globals, plain
// functions over plain structs, so every function here is trivially
// unit-testable without a war room).
package narrate

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

// Args is the arbitrary, JSON-shaped argument bag a tool call carries — the
// Go analogue of narrate.mjs's `args = {}` parameter. A nil Args is safe to
// read from (all lookups below tolerate it), matching the source's own
// `const a = args ?? {}` normalization.
type Args map[string]any

// Contribution is what contributionFor returns when a tool call produces a
// durable artifact for the shared timeline: a (kind, body) pair to post.
// Body holds one of the *Body structs below, chosen by Kind.
type Contribution struct {
	Kind string
	Body any
}

// FindingBody is the body for a "finding" contribution (post_finding, note).
type FindingBody struct {
	Text     string `json:"text"`
	Resource any    `json:"resource,omitempty"`
}

// QueryBody is the body for a "query" contribution (search_context).
type QueryBody struct {
	Source    string `json:"source"`
	Operation string `json:"operation"`
	Resource  string `json:"resource"`
}

// ActionBody is the body for an "action" contribution (propose_action).
type ActionBody struct {
	Description   string `json:"description"`
	DryRunPreview string `json:"dryRunPreview"`
}

// WidgetBody is the body for a "widget" contribution (post_widget).
type WidgetBody struct {
	WidgetType any    `json:"widgetType,omitempty"`
	Title      string `json:"title"`
	Data       any    `json:"data,omitempty"`
}

// Fields renders the contribution body as the flat key/value map the
// POST /edge/contributions envelope spreads over itself.
//
// The Node original's contributionFor returns a plain object that IS both the
// typed body and the thing spread onto the wire; Go's typed *Body structs buy
// the callers a checked shape, and this is the one place that converts back.
// It goes through the structs' own json tags rather than a hand-written map so
// the two spellings of the body can never drift apart. A body that cannot be
// marshalled yields nil, which the client sends as an empty object — the same
// thing JSON.stringify would have produced for an unrepresentable value.
func (c Contribution) Fields() map[string]any {
	encoded, err := json.Marshal(c.Body)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(encoded, &out); err != nil {
		return nil
	}
	return out
}

// NarrateDoing returns a friendly present-tense phrase for what the agent is
// doing right now, for a given tool name and call arguments. Ported 1:1 from
// narrate.mjs's narrateDoing switch.
func NarrateDoing(toolName string, args Args) string {
	switch toolName {
	case "describe_widget_types":
		return "checking which widget types the canvas renders"
	case "get_brief":
		return "reviewing the incident brief"
	case "get_updates":
		return "checking for new shared context"
	case "read_timeline":
		return "reading the incident timeline"
	case "search_context":
		if v, ok := truthyStr(args, "query"); ok {
			return fmt.Sprintf(`searching context for "%s"`, v)
		}
		return "searching context"
	case "post_finding":
		if v, ok := truthyStr(args, "text"); ok {
			return fmt.Sprintf("posting a finding: %s", truncate(v))
		}
		return "posting a finding"
	case "note":
		return fmt.Sprintf("noting: %s", truncate(nullishStr(args, "text")))
	case "propose_action":
		if v, ok := truthyStr(args, "description"); ok {
			return fmt.Sprintf("proposing a remediation: %s", truncate(v))
		}
		return "proposing a remediation"
	case "post_widget":
		if v, ok := truthyStr(args, "title"); ok {
			return fmt.Sprintf("building a dashboard widget: %s", truncate(v))
		}
		return "building a dashboard widget"
	case "upload_artifact":
		if v, ok := truthyStr(args, "filename"); ok {
			return fmt.Sprintf(`sharing an artifact: "%s"`, truncate(v))
		}
		return "sharing an artifact"
	case "flag_context":
		base := fmt.Sprintf("flagging #%s as wrong", rawJS(args, "targetSeq"))
		if v, ok := truthyStr(args, "reason"); ok {
			return fmt.Sprintf("%s: %s", base, truncate(v))
		}
		return base
	case "corroborate_claim":
		base := fmt.Sprintf("corroborating claim #%s", rawJS(args, "claimSeq"))
		if v, ok := truthyStr(args, "reason"); ok {
			return fmt.Sprintf("%s: %s", base, truncate(v))
		}
		return base
	case "contest_claim":
		base := fmt.Sprintf("contesting claim #%s", rawJS(args, "claimSeq"))
		if v, ok := truthyStr(args, "reason"); ok {
			return fmt.Sprintf("%s: %s", base, truncate(v))
		}
		return base
	case "stage_claim":
		if v, ok := truthyStr(args, "statement"); ok {
			return fmt.Sprintf("staging a claim: %s", truncate(v))
		}
		return "staging a claim"
	case "record_activity":
		if v, ok := args["doing"]; ok && v != nil {
			return jsString(v)
		}
		return "investigating"
	default:
		return fmt.Sprintf("using %s", toolName)
	}
}

// ContributionFor maps a tool call to the (kind, body) contribution it
// should post to the shared timeline, or nil when the call is read-only (or
// when the call's own endpoint already appends its own durable event).
//
// upload_artifact and all four vetting tools (flag_context,
// corroborate_claim, contest_claim, stage_claim) deliberately return nil:
// those endpoints append their OWN durable events server-side
// (`artifact.shared`, `context.flagged`, `claim.corroborated`/
// `claim.contested`, `claim.staged`). A generic contribution on top of those
// would double-post — and for the vetting tools specifically, it would also
// put an unvetted restatement of a staged claim straight into the feed the
// staging area exists to keep it out of. Presence is still narrated by the
// heartbeat (NarrateDoing) either way.
func ContributionFor(toolName string, args Args) *Contribution {
	switch toolName {
	case "post_finding", "note":
		return &Contribution{Kind: "finding", Body: FindingBody{
			Text:     nullishStr(args, "text"),
			Resource: args["resource"],
		}}
	case "search_context":
		return &Contribution{Kind: "query", Body: QueryBody{
			Source:    "edge",
			Operation: "search",
			Resource:  nullishStr(args, "query"),
		}}
	case "propose_action":
		return &Contribution{Kind: "action", Body: ActionBody{
			Description:   nullishStr(args, "description"),
			DryRunPreview: nullishStr(args, "dryRunPreview"),
		}}
	case "post_widget":
		return &Contribution{Kind: "widget", Body: WidgetBody{
			WidgetType: args["widgetType"],
			Title:      nullishStr(args, "title"),
			Data:       args["data"],
		}}
	case "upload_artifact":
		return nil
	case "flag_context", "corroborate_claim", "contest_claim", "stage_claim":
		return nil
	default:
		return nil // get_brief / read_timeline / record_activity → presence only
	}
}

// EventActor returns who published a shared event: the human display name
// (and edge agent label, if any), never the raw humanActorId.
func EventActor(payload Args) string {
	displayName, _ := payload["displayName"].(string)
	edgeAgentLabel, _ := payload["edgeAgentLabel"].(string)
	var parts []string
	if displayName != "" {
		parts = append(parts, displayName)
	}
	if edgeAgentLabel != "" {
		parts = append(parts, edgeAgentLabel)
	}
	return joinDot(parts)
}

func joinDot(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " · "
		}
		out += p
	}
	return out
}

// EventText returns the human-readable substance of a shared event.
//
// Vetting and claim events (context.flagged, context.voted, claim.staged,
// claim.contested) carry theirs in statement/reason/stance rather than
// text, so a reader that knew only the older fields rendered them as a bare
// type with no body — hence the fallback chain below, ported field-for-field
// from narrate.mjs's own chain.
func EventText(payload Args) string {
	for _, key := range []string{"text", "description", "doing", "summary", "statement", "reason", "stance"} {
		if v, ok := payload[key]; ok && v != nil {
			if s, ok := v.(string); ok {
				return s
			}
			return ""
		}
	}
	return ""
}

// IsTemplated reports whether this event's text is a fixed template with no
// real synthesis behind it (a chat-reaction's fixed-slot reply, e.g.) rather
// than genuine analysis.
//
// Feature 20260812-010632 (US5/T050a, FR-029): the server sets BOTH
// synthesized:false and the explicit disclosedAsTemplated:true marker
// together on exactly this content — checking the explicit marker, not just
// synthesized===false, matches the server's own distinction precisely: an
// event with neither field present is simply old data from before this
// marker existed, not a claim about whether it was synthesized. Reading
// synthesized alone would be a second, drifting interpretation of a field
// this package does not own.
func IsTemplated(payload Args) bool {
	v, ok := payload["disclosedAsTemplated"]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && b
}

// FormatEventLine renders one compact line per shared event, attributed to
// human · agent. The single renderer for every place a queued room event is
// spelled out.
//
// Takes internal/client's Event — the same struct the live socket parks on the
// session's pending queue, which is what the Node original's digest maps over
// (src/hooks/socket.mjs:169). A nil Seq renders as "undefined" rather than as
// "#0", since seq 0 is a real event.
func FormatEventLine(e client.Event) string {
	who := EventActor(e.Payload)
	what := EventText(e.Payload)
	marker := ""
	if IsTemplated(e.Payload) {
		marker = " (no evidence — templated, not analysis)"
	}
	line := fmt.Sprintf("#%s %s", seqOrUndefined(e.Seq), e.Type)
	if who != "" {
		line += fmt.Sprintf(" [%s]", who)
	}
	if what != "" {
		line += fmt.Sprintf(" — %s", what)
	}
	line += marker
	return line
}

// truncate trims s to n runes (default 80, matching narrate.mjs's truncate),
// appending an ellipsis when it had to cut.
func truncate(s string) string {
	return truncateN(s, 80)
}

// DescribeEvent is one human-readable stderr line for a LIVE-pushed shared
// event — src/live.mjs's describeEvent, the doorbell nudge a responder sees
// the moment another investigator publishes, distinct from FormatEventLine's
// digest-line format ("#<seq> <type> [who] — text") used when catching up on
// a batch. describeEvent has its own 100-rune truncation (not the 80-rune
// default truncate above) and always ends with the same call-to-action,
// because get_updates — not this line — is what the agent actually acts on;
// this exists only so a human watching the terminal isn't left guessing.
func DescribeEvent(e client.Event) string {
	who := EventActor(e.Payload)
	what := EventText(e.Payload)
	body := ""
	if what != "" {
		body = fmt.Sprintf(": %q", truncateN(what, 100))
	}
	from := ""
	if who != "" {
		from = " from " + who
	}
	evtType := e.Type
	if evtType == "" {
		evtType = "event" // evt?.type ?? 'event' — live.mjs's own fallback
	}
	return fmt.Sprintf("⚡ %s%s%s — new shared context; call get_updates.", evtType, from, body)
}

func truncateN(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// truthyStr reads args[key] and reports whether it is JS-truthy (present,
// non-nil, and — for the scalar kinds tool args actually carry — non-empty/
// non-zero/non-false), returning its String()-coerced value when so. This
// mirrors the `a.field ? ... : ”` guard used throughout narrateDoing.
func truthyStr(args Args, key string) (string, bool) {
	v, ok := args[key]
	if !ok || v == nil {
		return "", false
	}
	switch t := v.(type) {
	case string:
		if t == "" {
			return "", false
		}
		return t, true
	case bool:
		if !t {
			return "", false
		}
		return jsString(t), true
	case float64:
		if t == 0 {
			return "", false
		}
		return jsString(t), true
	default:
		// Objects/arrays are always truthy in JS, even when empty.
		return jsString(t), true
	}
}

// nullishStr reads args[key] and returns its String()-coerced value when
// present and non-nil, else "" — the Go analogue of `a.field ?? ”`.
func nullishStr(args Args, key string) string {
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	return jsString(v)
}

// rawJS returns the String()-coercion of args[key] with no presence guard at
// all — the Go analogue of a bare `${a.field}` template interpolation, which
// prints the literal "undefined" when the key is absent (as narrate.mjs's
// own flag_context/corroborate_claim/contest_claim cases do for
// targetSeq/claimSeq).
func rawJS(args Args, key string) string {
	v, ok := args[key]
	if !ok {
		return "undefined"
	}
	return jsString(v)
}

// jsString coerces an arbitrary decoded-JSON value to a string the way
// JavaScript's String() would for the value kinds tool args actually carry.
func jsString(v any) string {
	if v == nil {
		return "undefined"
	}
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		if !math.IsInf(t, 0) && t == math.Trunc(t) && math.Abs(t) < 1e15 {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	default:
		return fmt.Sprintf("%v", t)
	}
}
