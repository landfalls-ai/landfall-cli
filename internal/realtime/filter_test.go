package realtime

import (
	"testing"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

func ev(typ string, payload map[string]any) client.Event {
	return client.Event{Type: typ, Payload: payload}
}

func TestIsQuiet(t *testing.T) {
	quiet := []string{
		"edge.participant.heartbeat",
		"edge.participant.joined",
		"edge.participant.left",
		"edge.ticket.redeemed",
	}
	for _, typ := range quiet {
		if !IsQuiet(typ) {
			t.Errorf("IsQuiet(%q) = false, want true", typ)
		}
	}
	if len(QuietTypes) != len(quiet) {
		t.Errorf("QuietTypes has %d entries, want exactly %d", len(QuietTypes), len(quiet))
	}

	loud := []string{
		"finding.published",
		"note.added",
		"claim.staged",
		"edge.participant.other",
		"", // `QUIET_TYPES.has(undefined)` is false: a typeless event is delivered
	}
	for _, typ := range loud {
		if IsQuiet(typ) {
			t.Errorf("IsQuiet(%q) = true, want false", typ)
		}
	}
}

func TestEventInstanceID(t *testing.T) {
	cases := []struct {
		name string
		evt  client.Event
		want string
	}{
		{"present", ev("x", map[string]any{"agentInstanceId": "me-1"}), "me-1"},
		{"no payload", ev("x", nil), ""},
		{"absent key", ev("x", map[string]any{"other": "v"}), ""},
		{"empty string", ev("x", map[string]any{"agentInstanceId": ""}), ""},
		// `===` against a string id can never match a number or an object,
		// so a non-string reads as "no id" rather than being coerced.
		{"numeric", ev("x", map[string]any{"agentInstanceId": float64(7)}), ""},
		{"nested", ev("x", map[string]any{"agentInstanceId": map[string]any{}}), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EventInstanceID(tc.evt); got != tc.want {
				t.Errorf("EventInstanceID = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestShouldDeliver(t *testing.T) {
	cases := []struct {
		name string
		evt  client.Event
		own  string
		want bool
	}{
		{"someone else's finding", ev("finding.published", map[string]any{"agentInstanceId": "them"}), "me", true},
		{"our own finding", ev("finding.published", map[string]any{"agentInstanceId": "me"}), "me", false},
		{"quiet beats everything", ev("edge.participant.heartbeat", map[string]any{"agentInstanceId": "them"}), "me", false},
		{"quiet and ours", ev("edge.participant.left", map[string]any{"agentInstanceId": "me"}), "me", false},
		{"no own id yet", ev("finding.published", map[string]any{"agentInstanceId": "me"}), "", true},
		{"no payload", ev("finding.published", nil), "me", true},
		{"typeless is delivered", ev("", nil), "me", true},
		{"numeric instance id never matches", ev("note.added", map[string]any{"agentInstanceId": float64(1)}), "1", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShouldDeliver(tc.evt, tc.own); got != tc.want {
				t.Errorf("ShouldDeliver = %v, want %v", got, tc.want)
			}
		})
	}
}
