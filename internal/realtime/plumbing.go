package realtime

import (
	"strings"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

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
	// edge.interrupt.delivered: the server recording that an interrupt reached an
	// agent; a receipt, not a message. Three of these filled a prompt digest in
	// the v0.8.7 idle run (specs/20260922-local-room-daemon/verification-deployed.md).
	"edge.interrupt.",
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

// investigatorNewsTypes is what counts as NEWS for a person or an agent that
// is between tool calls: the event types the server's own delta classifier
// (the context-frame package's SUBSTANTIVE_TYPES and
// ADDRESSED_INCIDENT_TYPES) surfaces, plus the claim and vetting moves a
// Stop hook has always had to wait for.
//
// AN ALLOW-LIST, UNLIKE plumbingPrefixes ABOVE. The deny-list let every new
// kind of room machinery through until somebody noticed it: on 2026-09-28 a
// Stop hook refused a conclusion over `oncall.schedule.resolved` and
// `voice.recording_started`, neither of which any person or agent needed to
// read. The server made the same call long ago (an unlisted type is routine
// in the delta); the hooks now agree with it. Everything else still advances
// the cursor when a hook consumes, so nothing is delivered twice, it is just
// never a reason to interrupt.
var investigatorNewsTypes = map[string]struct{}{
	"edge.finding":            {},
	"agent.finding":           {},
	"claim.staged":            {},
	"claim.admitted":          {},
	"claim.demoted":           {},
	"claim.corroborated":      {},
	"claim.contested":         {},
	"agent.hypothesis.raised": {},
	"artifact.shared":         {},
	"edge.capture.summarized": {},
	"line.claimed":            {},
	"line.released":           {},
	"line.transferred":        {},
	"line.ruled_out":          {},
	"voice.segment":           {},
	"context.attached":        {},
	"context.detached":        {},
	"context.flagged":         {},
	"steer.recorded":          {},
	"memory.proposed":         {},
	"related.suggested":       {},
	"memory.proposal.decided": {},
	"incident.triggered":      {},
	"status.changed":          {},
	"severity.changed":        {},
}

// investigatorNewsPrefixes are families that are news as a whole.
var investigatorNewsPrefixes = []string{"proposal."}

// centralAgentNames are the display names the platform stamps on Beacon's own
// rows (the server's CENTRAL_AGENT_ALIASES).
var centralAgentNames = map[string]struct{}{
	"Beacon":          {},
	"Diagnosis Agent": {},
	"diagnosis-agent": {},
}

// IsInvestigatorNews reports whether an event is something a person or an
// agent in the room should be interrupted for. A chat message counts only
// when a human wrote it: an agent's chat line reaches the other agents on
// their next read and is not a reason to stop anyone.
func IsInvestigatorNews(eventType string, payload map[string]any) bool {
	return isInvestigatorNews(eventType, "", payload)
}

// IsNews is IsInvestigatorNews for a whole event, which also knows the row's
// actorType.
func IsNews(evt client.Event) bool {
	return isInvestigatorNews(evt.Type, evt.ActorType, evt.Payload)
}

func isInvestigatorNews(eventType, actorType string, payload map[string]any) bool {
	if eventType == "chat.message" {
		return isHumanAuthored(actorType, payload)
	}
	if _, ok := investigatorNewsTypes[eventType]; ok {
		return true
	}
	for _, p := range investigatorNewsPrefixes {
		if strings.HasPrefix(eventType, p) {
			return true
		}
	}
	return false
}

// isHumanAuthored: no agent instance, no agent label, not the central agent,
// and not a row the server itself says a system or an agent wrote. An absent
// actorType with none of the agent markers reads as a person, which is how
// every chat row looked before the server carried the field.
func isHumanAuthored(actorType string, payload map[string]any) bool {
	switch actorType {
	case "system", "agent":
		return false
	}
	if payload == nil {
		return true
	}
	if id, _ := payload["agentInstanceId"].(string); id != "" {
		return false
	}
	if label, _ := payload["edgeAgentLabel"].(string); label != "" {
		return false
	}
	if kind, _ := payload["kind"].(string); kind == "agent" {
		return false
	}
	if name, _ := payload["displayName"].(string); name != "" {
		if _, beacon := centralAgentNames[name]; beacon {
			return false
		}
	}
	return true
}
