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
//	       │
//	    success
//	       │
//	       ▼
//	 [Published] ──► compacted out on Ack
//
// There is deliberately NO terminal Failed state. A permanently failing entry
// stays Queued with a rising attempt count and a visible LastError, because
// silently discarding a responder's finding is the worst outcome available to
// this package. Overflow (FR-012) is the only path that ever drops, and it
// counts what it dropped.
type State string

const (
	Queued     State = "queued"
	Publishing State = "publishing"
	Published  State = "published"
	Abandoned  State = "abandoned"
)

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

	State    State `json:"state"`
	Attempts int   `json:"attempts"`

	CreatedAt time.Time `json:"created_at"`
	// LastError is kept for the operator and surfaced on stderr only — serve's
	// stdout is the MCP wire (FR-011).
	LastError string `json:"last_error,omitempty"`
}
