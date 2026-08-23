package realtime

import "github.com/landfalls-ai/landfall-cli/internal/client"

// The two filters `live.mjs` applies between the socket and the session queue.
// They are the whole reason `watchIncident` took an injectable `ioImpl` in the
// Node original (src/live.mjs:26) — a real socket makes them untestable — so
// they live here as pure functions with no socket anywhere near them.

// QuietTypes are the event types that are pure presence plumbing: noise for a
// teammate, never worth a doorbell. Mirrors `QUIET_TYPES` (src/live.mjs:12-17).
var QuietTypes = map[string]struct{}{
	"edge.participant.heartbeat": {},
	"edge.participant.joined":    {},
	"edge.participant.left":      {},
	"edge.ticket.redeemed":       {},
}

// IsQuiet reports whether an event type is presence plumbing.
//
// An empty type is deliberately NOT quiet: the Node original tests
// `QUIET_TYPES.has(evt.type)`, and `has(undefined)` is false, so a typeless
// event was delivered rather than dropped (src/live.mjs:36).
func IsQuiet(eventType string) bool {
	_, ok := QuietTypes[eventType]
	return ok
}

// EventInstanceID reads `payload.agentInstanceId` off an event, returning ""
// when the payload is absent, is not an object, carries no such key, or
// carries one that is not a string.
//
// The string requirement is faithful, not defensive: the Node original
// compares with `===` against this client's own id (a string), so a
// numeric or object `agentInstanceId` would never have matched either
// (src/live.mjs:37-39).
func EventInstanceID(evt client.Event) string {
	if evt.Payload == nil {
		return ""
	}
	id, _ := evt.Payload["agentInstanceId"].(string)
	return id
}

// ShouldDeliver applies both filters in the Node original's order: drop quiet
// types outright, then drop anything this agent instance published itself so
// the bridge never echoes its own writes back at its own agent.
//
// An empty ownInstanceID disables echo suppression entirely, matching
// `if (own && instance === own)` (src/live.mjs:39) — before the bridge has
// joined it has no id, and "unknown id" must never be read as "matches".
func ShouldDeliver(evt client.Event, ownInstanceID string) bool {
	if IsQuiet(evt.Type) {
		return false
	}
	if ownInstanceID != "" && EventInstanceID(evt) == ownInstanceID {
		return false
	}
	return true
}
