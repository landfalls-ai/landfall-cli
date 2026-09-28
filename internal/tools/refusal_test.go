package tools

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// refusingAccepter is an Accepter whose queue holds refusals the agent has not
// been told about, told once each, exactly like the spool-backed one.
type refusingAccepter struct {
	fakeAccepter
	mu      sync.Mutex
	pending map[string][]Refusal
	asked   []string
}

func (r *refusingAccepter) TakeRefusals(incidentID string) []Refusal {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked = append(r.asked, incidentID)
	out := r.pending[incidentID]
	delete(r.pending, incidentID)
	return out
}

func newRefusing(rs ...Refusal) *refusingAccepter {
	return &refusingAccepter{pending: map[string][]Refusal{"inc-1": rs}}
}

// TestARefusedShareIsToldOnTheNextRoomToolResult: the agent was told
// "shared"; the room's refusal is the only correction it will ever get, so it
// rides the very next room tool result, once.
func TestARefusedShareIsToldOnTheNextRoomToolResult(t *testing.T) {
	acc := newRefusing(Refusal{Text: "the root cause is the origin rollback at 14:20 on the checkout fleet", Reason: "statement required"})
	list := BuildWithAccepter(newSession(&fakeClient{}), acc)

	out := callTool(t, list, "get_brief", map[string]any{})
	want := `Your earlier share did not reach the room: statement required (it began "the root cause is the origin rollback at 14:20 on the checko…").`
	if !strings.HasPrefix(out, want) {
		t.Fatalf("result does not open with the refusal:\n%s", out)
	}
	if !strings.Contains(out, "It will not be retried.") {
		t.Errorf("the agent is not told the refusal is final:\n%s", out)
	}

	again := callTool(t, list, "get_brief", map[string]any{})
	if strings.Contains(again, "did not reach the room") {
		t.Fatalf("the refusal was told twice:\n%s", again)
	}
	if acc.asked[0] != "inc-1" {
		t.Errorf("refusals were asked for incident %q, want the joined room's", acc.asked[0])
	}
}

// TestShareWithRoomAlsoCarriesAnEarlierRefusal: share_with_room is a room
// tool too, and often the very next call after the refused one.
func TestShareWithRoomAlsoCarriesAnEarlierRefusal(t *testing.T) {
	acc := newRefusing(Refusal{Text: "chart: p99", Reason: `widget data does not match the "chart" contract: chart.series must be an array`})
	tool := shareTool(t, newSession(&fakeClient{}), acc)
	out, err := tool.Handler(context.Background(), map[string]any{"text": "origin returned 502"})
	if err != nil {
		t.Fatalf("share: %v", err)
	}
	if !strings.Contains(out, `Your earlier share did not reach the room: widget data does not match the "chart" contract`) {
		t.Fatalf("no refusal line:\n%s", out)
	}
	if !strings.Contains(out, "shared — the room will have this shortly") {
		t.Fatalf("the new share's own confirmation is missing:\n%s", out)
	}
}

// TestManyRefusalsAreCapped: a burst of refusals must not swamp the result.
func TestManyRefusalsAreCapped(t *testing.T) {
	var rs []Refusal
	for i := 0; i < refusalCap+3; i++ {
		rs = append(rs, Refusal{Text: "x", Reason: "no"})
	}
	out := RenderRefusals(rs)
	if n := strings.Count(out, "did not reach the room"); n != refusalCap {
		t.Fatalf("named %d refusals, want %d", n, refusalCap)
	}
	if !strings.Contains(out, "And 3 more the room refused.") {
		t.Fatalf("the rest are not counted:\n%s", out)
	}
	if RenderRefusals(nil) != "" {
		t.Fatal("no refusals must render nothing")
	}
}

// TestAnAccepterWithoutRefusalsChangesNothing: the capability is optional;
// a plain Accepter (or none) leaves every result exactly as it was.
func TestAnAccepterWithoutRefusalsChangesNothing(t *testing.T) {
	plain := BuildWithAccepter(newSession(&fakeClient{}), &fakeAccepter{})
	none := Build(newSession(&fakeClient{}))
	a := callTool(t, plain, "get_brief", map[string]any{})
	b := callTool(t, none, "get_brief", map[string]any{})
	if a != b {
		t.Fatalf("a plain accepter changed get_brief:\n%s\nvs\n%s", a, b)
	}
}
