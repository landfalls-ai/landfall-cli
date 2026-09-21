package realtime

import "testing"

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
