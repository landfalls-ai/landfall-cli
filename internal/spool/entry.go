// Package spool is the bridge's durable outbound queue: the local record of
// what the responder's agent handed off with share_with_room but that has not
// reached the room yet.
//
// WHY IT EXISTS. share_with_room must return without touching the network
// (spec FR-001) — an agent mid-investigation cannot wait on a round trip. So
// the hand-off has to land somewhere durable before the call returns, and a
// background worker publishes it later. That "somewhere" is this package.
//
// DURABILITY IS THE WHOLE POINT (FR-008). A responder's finding must survive
// the process dying. Two consequences that are easy to get wrong:
//
//   - Accept fsyncs before returning. A buffered write that loses the tail on
//     power failure would satisfy the type signature and fail the requirement.
//   - The spool does NOT live under the hook socket's directory. That is
//     runtimeDir (internal/hooks/socket.go:151-162) — XDG_RUNTIME_DIR, which is
//     tmpfs on Linux and wiped on reboot. Putting a durable queue there would
//     build this feature's exact failure mode. See stateDir below.
//
// EXACTLY-ONCE IS NOT LOCAL. This package delivers at-least-once. A crash
// between "the server accepted it" and "we wrote the ack" replays the entry.
// Suppressing that duplicate is internal/bridge's job, using the local mirror
// to ask whether the entry already landed (spec D9) — the edge endpoints do not
// accept a client idempotency key, so it cannot be solved by sending one.
package spool

import "time"

// State is where an entry is in its life. The transitions are:
//
//	    Accept
//	       │
//	       ▼
//	   [Queued] ────── room changed ──────► [Abandoned]
//	       │
//	worker picks up
//	       │
//	       ▼
//	 [Publishing] ── transient failure ──► [Queued] (attempts++)
//	       │ └────── the room refused it (4xx) ──► [Refused]
//	    success
//	       │
//	       ▼
//	 [Published] ──► compacted out on Ack
//
// A transient failure (the room unreachable, a timeout, a 5xx, the room's rate
// limit) requeues: the same hand-off will likely land later. A REFUSAL does
// not. The room answered 400 "statement required" to every claim this bridge
// ever staged, and the entry was retried every five seconds for the rest of
// the session while the agent had been told "shared" (found 2026-09-28). An
// entry the room refuses is therefore Refused: kept, never retried, and told
// to the agent on its next room tool result and to the person on the status
// line, so whoever can fix it knows to. Nothing is discarded silently: the
// entry and the room's reason stay in the file.
type State string

const (
	Queued     State = "queued"
	Publishing State = "publishing"
	Published  State = "published"
	Abandoned  State = "abandoned"
	// Refused is terminal: the room answered this hand-off with a 4xx that
	// asking again, unchanged, will only get again (see client.Refusal). The
	// worker never retries it; Refusal records the room's reason.
	Refused State = "refused"
	// Held is a hand-off the room daemon stopped because it names the person's
	// working directory (feature 20260922-local-room-daemon, FR-008). It is
	// not queued, so the worker never publishes it; it leaves this state only
	// by the person's hand: `landfall allow-cwd` → Queued, `landfall held drop`
	// → Abandoned. A room change abandons it exactly as it abandons Queued.
	Held State = "held"
)

// HeldReason is why an entry is Held: what in its text matched the working
// directory, shown to the person verbatim so the hold is explainable.
type HeldReason struct {
	Matched []string  `json:"matched"`
	HeldAt  time.Time `json:"held_at"`
}

// Entry is one hand-off awaiting publication.
type Entry struct {
	// ID is generated at Accept, before share_with_room returns. It is the
	// key the bridge reconciles against after a crash (D9), so it must be
	// stable for the life of the entry.
	ID string `json:"id"`

	// IncidentID binds the entry to one room. An entry is only ever
	// publishable into the room it was created for — join_war_room can
	// re-target the session mid-process, and a finding meant for room A must
	// never surface in room B.
	IncidentID string `json:"incident_id"`

	// AgentInstanceID is the instance that handed it off. Carried for
	// provenance (FR-004) and used by D9's reconciliation to recognize its
	// own writes in the timeline.
	AgentInstanceID string `json:"agent_instance_id"`

	Text string   `json:"text"`
	Refs []string `json:"refs,omitempty"`

	// Kind is the caller's OWN say on how this publishes ("note", "finding",
	// "claim", "widget"), when it gave one. Empty means the worker classifies
	// from the text as it always has. An explicit kind is a stronger signal
	// than any marker: the agent said what this is, in the schema itself.
	Kind string `json:"kind,omitempty"`

	// Held is set while State == Held (see HeldReason); nil otherwise.
	Held *HeldReason `json:"held,omitempty"`

	// Widget carries the structured values for a widget hand-off, when the
	// caller supplied them explicitly (widgetType/title/data), rather than
	// leaving the worker to infer one from free text. Nil for every other
	// kind, and nil for a widget hand-off with no structured payload, which
	// the worker publishes as a note (internal/bridge's kindFor).
	Widget *WidgetPayload `json:"widget,omitempty"`

	State    State `json:"state"`
	Attempts int   `json:"attempts"`

	CreatedAt time.Time `json:"created_at"`
	// LastError is kept for the operator and surfaced on stderr only — serve's
	// stdout is the MCP wire (FR-011).
	LastError string `json:"last_error,omitempty"`

	// Redacted records that Accept altered the text. The responder is TOLD
	// this rather than having their words quietly rewritten: something they
	// believed they shared is not what the room will see, and finding that out
	// later, from the timeline, would be worse.
	Redacted bool `json:"redacted,omitempty"`

	// Refusal is the room's own reason for refusing this hand-off, set when
	// State == Refused ("statement required", the widget contract error).
	Refusal string `json:"refusal,omitempty"`
	// RefusedAt is when the room refused it. The status line counts recent
	// refusals only (RefusedWindow), so an old one does not linger there.
	RefusedAt *time.Time `json:"refused_at,omitempty"`
	// Reported records that the agent was told about the refusal, on a room
	// tool result. Each refusal is told once.
	Reported bool `json:"reported,omitempty"`

	// SourceQueryFailed is the caller's OWN self-report, at hand-off time,
	// that this text/widget was produced after one of its own tool calls
	// failed (a service the local environment doesn't emulate, a malformed
	// response, a transient disconnect). Never independently verified — the
	// same trust level already given to the hand-off's text itself. Carried
	// through to publication so the room's admission gate can screen it
	// (Landfall feature 20260920-132909); absent/false for every hand-off
	// that doesn't set it, which is every hand-off from before this field
	// existed.
	SourceQueryFailed bool `json:"source_query_failed,omitempty"`
}

// WidgetPayload is the structured content of a widget hand-off — the values
// an edge agent computed itself, passed straight through to the room rather
// than reconstructed from a free-text marker (see classify.go's widgetMarkers
// for why a marker alone cannot carry this).
type WidgetPayload struct {
	WidgetType string         `json:"widgetType"`
	Title      string         `json:"title"`
	Data       map[string]any `json:"data,omitempty"`
}
