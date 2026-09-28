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

// TestARefusalCorrectingCannotFixSaysSo: "share it again, corrected" is only
// true when the room named something in the share. A closed engagement, a
// forbidden or a missing room answers a corrected share the same way.
func TestARefusalCorrectingCannotFixSaysSo(t *testing.T) {
	closed := RenderRefusals([]Refusal{{Text: "the root cause is the rollback", Reason: "engagement is closed; admission is frozen", Status: 409}})
	if strings.Contains(closed, "corrected") {
		t.Errorf("a closed engagement is told to share again, corrected:\n%s", closed)
	}
	if !strings.Contains(closed, "Sharing it again will get the same answer; tell the person.") {
		t.Errorf("a final refusal does not say sharing again is pointless:\n%s", closed)
	}
	for _, status := range []int{403, 404} {
		out := RenderRefusals([]Refusal{{Text: "x", Reason: "no", Status: status}})
		if strings.Contains(out, "corrected") {
			t.Errorf("HTTP %d is told to share again, corrected:\n%s", status, out)
		}
	}

	bad := RenderRefusals([]Refusal{{Text: "chart: p99", Reason: "statement required", Status: 400}})
	if !strings.Contains(bad, "It will not be retried. Share it again, corrected, if the room still needs it.") {
		t.Errorf("a 400 lost its correction advice:\n%s", bad)
	}

	mixed := RenderRefusals([]Refusal{
		{Text: "a", Reason: "statement required", Status: 400},
		{Text: "b", Reason: "engagement is closed; admission is frozen", Status: 409},
	})
	if !strings.Contains(mixed, "None of these will be retried.") ||
		!strings.Contains(mixed, "Where the room named something to fix") ||
		!strings.Contains(mixed, "sharing again gets the same answer; tell the person") {
		t.Errorf("a mixed batch does not tell the two apart:\n%s", mixed)
	}
}

// expiringAccepter is an Accepter whose room session can be expired, as the
// spool-backed one reports it after the room answers 401.
type expiringAccepter struct {
	fakeAccepter
	mu      sync.Mutex
	expired bool
	waiting int
}

func (e *expiringAccepter) SessionExpired(string) (int, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.waiting, e.expired
}

func (e *expiringAccepter) set(expired bool, waiting int) {
	e.mu.Lock()
	e.expired, e.waiting = expired, waiting
	e.mu.Unlock()
}

// TestAnExpiredSessionIsNotReportedAsShared: after 8 hours the room answers
// 401 to everything this session sends. share_with_room must not say "the room
// will have this shortly", and must not tell the agent to correct anything:
// the share waits on this machine for the person to rejoin.
func TestAnExpiredSessionIsNotReportedAsShared(t *testing.T) {
	acc := &expiringAccepter{}
	acc.set(true, 3)
	tool := shareTool(t, newSession(&fakeClient{}), acc)
	out, err := tool.Handler(context.Background(), map[string]any{"text": "origin returned 502"})
	if err != nil {
		t.Fatalf("share: %v", err)
	}
	if strings.Contains(out, "shared — the room will have this shortly") {
		t.Fatalf("an expired session was reported as shared:\n%s", out)
	}
	for _, want := range []string{"queued on this machine, not in the room yet", "session has expired", "rejoins with a new link", "other 2 waiting"} {
		if !strings.Contains(out, want) {
			t.Errorf("result lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "corrected") {
		t.Errorf("an expired session is worded as something to correct:\n%s", out)
	}
	if acc.calls != 1 {
		t.Errorf("the share was not kept (%d accepts)", acc.calls)
	}
}

// TestTheExpiryIsToldOnceUntilItClears: every room tool fails while the
// session is expired; the agent hears why once, not on every result, and
// hears it again if a later session expires too.
func TestTheExpiryIsToldOnceUntilItClears(t *testing.T) {
	acc := &expiringAccepter{}
	acc.set(true, 1)
	list := BuildWithAccepter(newSession(&fakeClient{}), acc)

	first := callTool(t, list, "get_brief", map[string]any{})
	if !strings.HasPrefix(first, "The room session has expired. Your share is waiting on this machine (1)") {
		t.Fatalf("the first result does not open with the expiry:\n%s", first)
	}
	if strings.Contains(first, "corrected") {
		t.Errorf("an expired session is worded as something to correct:\n%s", first)
	}
	if again := callTool(t, list, "get_brief", map[string]any{}); strings.Contains(again, "session has expired") {
		t.Fatalf("the expiry was told twice:\n%s", again)
	}

	acc.set(false, 0) // the person rejoined; everything landed
	if clear := callTool(t, list, "get_brief", map[string]any{}); strings.Contains(clear, "session has expired") {
		t.Fatalf("a valid session still reads as expired:\n%s", clear)
	}
	acc.set(true, 2) // eight hours later
	if later := callTool(t, list, "get_brief", map[string]any{}); !strings.Contains(later, "Your shares are waiting on this machine (2)") {
		t.Fatalf("a second expiry was not told:\n%s", later)
	}
}
