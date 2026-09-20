package realtime

import "strings"

// Plumbing is the second filter, and it is applied at a DIFFERENT seam from
// QuietTypes. QuietTypes decides what never enters the session queue at all
// (presence: nobody wants a doorbell for a heartbeat). Plumbing decides what,
// once queued, is NOT worth interrupting a human's own work for: the room's
// machinery talking to itself — Beacon's telemetry queries, widget build
// stages, the pull-ledger rows the server writes when an agent reads.
//
// The distinction matters because the two hooks that speak to the model
// (`stop`, `user-prompt-submit`) were counting every queued event as an
// "update from other investigators". Measured on 2026-09-20 with a real
// interactive Claude Code session (the monorepo's terminal harness): every one
// of three Stop-hook refusals in a run cited only `agent.query` rows, and four
// of five prompt digests carried nothing else. Each refusal cost the person a
// red error line, extra tool calls and a restated answer, over events nobody
// needed to read. Plumbing still advances the cursor when the hook consumes,
// so nothing is delivered twice; it simply stops being a reason to interrupt.
//
// A DENY-LIST, NOT AN ALLOW-LIST. An event type this file has never heard of
// is delivered. Under-reporting a teammate's finding is the failure this whole
// package exists to prevent; over-reporting a new kind of plumbing costs one
// nag and a one-line addition here.
var plumbingPrefixes = []string{
	"agent.query",
	"agent.widget.",
	"agent.reaction",
	"agent.run.",
	"agent.tool.",
	"governance.",
	"edge.query",
	"edge.participant.",
	"edge.ticket.",
	"widget.",
	"presence.",
	"canvas.",
}

// IsPlumbing reports whether an event type is room machinery rather than
// something a teammate said, found, decided or asked.
func IsPlumbing(eventType string) bool {
	for _, p := range plumbingPrefixes {
		if strings.HasPrefix(eventType, p) {
			return true
		}
	}
	return false
}
