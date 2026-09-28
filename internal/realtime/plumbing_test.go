package realtime

import (
	"testing"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

func TestIsPlumbingNamesTheRoomsOwnMachinery(t *testing.T) {
	plumbing := []string{
		"agent.query",
		"agent.widget.requested",
		"agent.widget.executed",
		"agent.widget.rendered",
		"agent.reaction",
		"agent.run.concluded",
		"governance.pulled",
		"edge.query",
		"edge.participant.heartbeat",
		"edge.ticket.redeemed",
		"edge.interrupt.delivered",
		"widget.pinned",
	}
	for _, typ := range plumbing {
		if !IsPlumbing(typ) {
			t.Errorf("IsPlumbing(%q) = false, want true", typ)
		}
	}
}

func TestIsPlumbingNeverHidesWhatATeammateSaidFoundOrAsked(t *testing.T) {
	substantive := []string{
		"chat.message",
		"edge.finding",
		"agent.finding",
		"agent.message",
		"agent.hypothesis",
		"claim.staged",
		"claim.admitted",
		"claim.quarantined",
		"artifact.shared",
		"edge.action.proposed",
		"remediation.proposed",
		"status.changed",
		"incident.opened",
		"voice.segment",
		// An unknown type is delivered: under-reporting is the failure mode.
		"something.new",
		"",
	}
	for _, typ := range substantive {
		if IsPlumbing(typ) {
			t.Errorf("IsPlumbing(%q) = true — a teammate's event would be hidden from the hooks", typ)
		}
	}
}

func TestIsInvestigatorNewsIsTheServersSubstantiveSet(t *testing.T) {
	news := []string{
		"edge.finding", "agent.finding", "claim.staged", "claim.admitted", "claim.demoted",
		"claim.corroborated", "claim.contested", "agent.hypothesis.raised", "artifact.shared",
		"edge.capture.summarized", "line.claimed", "line.released", "line.transferred",
		"line.ruled_out", "voice.segment", "context.attached", "context.detached",
		"steer.recorded", "memory.proposed", "related.suggested", "memory.proposal.decided",
		"incident.triggered", "status.changed", "severity.changed", "proposal.opened",
		"proposal.withdrawn", "context.flagged",
	}
	for _, typ := range news {
		if !IsInvestigatorNews(typ, map[string]any{}) {
			t.Errorf("IsInvestigatorNews(%q) = false, want true", typ)
		}
	}
}

func TestIsInvestigatorNewsIgnoresSystemNoiseAnAllowListNeverHeardOf(t *testing.T) {
	noise := []string{
		"oncall.schedule.resolved",
		"voice.recording_started",
		"voice.recording_stopped",
		"agent.query",
		"agent.step",
		"chat.reaction",
		"beacon.listening.changed",
		"edge.participant.joined",
		"presence.updated",
		"some.type.nobody.has.invented.yet",
		"",
	}
	for _, typ := range noise {
		if IsInvestigatorNews(typ, map[string]any{}) {
			t.Errorf("IsInvestigatorNews(%q) = true, want false", typ)
		}
	}
}

func TestAChatMessageIsNewsOnlyWhenAPersonWroteIt(t *testing.T) {
	human := client.Event{Type: "chat.message", ActorType: "human", Payload: map[string]any{"text": "the TTL is 60s", "displayName": "Maya"}}
	if !IsNews(human) {
		t.Fatal("a person's chat message is news")
	}
	legacy := client.Event{Type: "chat.message", Payload: map[string]any{"text": "no actorType on this row"}}
	if !IsNews(legacy) {
		t.Fatal("a chat row with no agent marker reads as a person's")
	}
	for name, evt := range map[string]client.Event{
		"edge agent":  {Type: "chat.message", ActorType: "human", Payload: map[string]any{"text": "x", "agentInstanceId": "inst-2"}},
		"agent label": {Type: "chat.message", Payload: map[string]any{"text": "x", "edgeAgentLabel": "Codex"}},
		"beacon":      {Type: "chat.message", Payload: map[string]any{"text": "x", "displayName": "Beacon"}},
		"system row":  {Type: "chat.message", ActorType: "system", Payload: map[string]any{"text": "x"}},
		"agent kind":  {Type: "chat.message", Payload: map[string]any{"text": "x", "kind": "agent"}},
		"agent actor": {Type: "chat.message", ActorType: "agent", Payload: map[string]any{"text": "x"}},
	} {
		if IsNews(evt) {
			t.Errorf("%s: an agent's chat line must not count as news", name)
		}
	}
	if !IsInvestigatorNews("chat.message", nil) {
		t.Fatal("a chat message with no payload has no agent marker")
	}
}
