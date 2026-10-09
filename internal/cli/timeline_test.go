package cli

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

const eventsPath = "/o/acme/incidents/inc-1/events"

func TestTimelineAllFoldsStepsAndReachesTheStart(t *testing.T) {
	f := newFakeLandfall(t)
	f.serveEvents(eventsPath, []map[string]any{
		{"seq": 0, "type": "incident.triggered", "actorType": "system", "occurredAt": "2026-10-08T15:52:00Z", "payload": map[string]any{"source": "Datadog", "text": "5xx over 2% for 5 minutes"}},
		{"seq": 1, "type": "agent.run.started", "payload": map[string]any{}},
		{"seq": 2, "type": "agent.step", "payload": map[string]any{"text": "reading CloudFront", "step": 1}},
		{"seq": 3, "type": "agent.widget.signed", "payload": map[string]any{"widgetId": "w1"}},
		{"seq": 4, "type": "agent.step", "payload": map[string]any{"text": "comparing target groups", "step": 2}},
		{"seq": 5, "type": "agent.step", "payload": map[string]any{"text": "origin-b in us-east-1c", "step": 3}},
		{"seq": 6, "type": "claim.staged", "payload": map[string]any{"statement": "5xx starts at 15:45Z", "displayName": "bob", "edgeAgentLabel": "Claude Code"}},
		{"seq": 7, "type": "chat.message", "payload": map[string]any{"text": "looking at eu-west-1", "displayName": "dave"}},
	})
	ans := roundTrip(t, RunTimeline(context.Background(), TimelineOptions{Before: -1, Limit: 50, Kind: "all"}, f.deps(true)))
	if ans["ok"] != true || ans["hasMore"] != false {
		t.Fatalf("answer: %v", ans)
	}
	rows := ans["events"].([]any)
	var seqs []float64
	for _, r := range rows {
		seqs = append(seqs, r.(map[string]any)["seq"].(float64))
	}
	// Bookkeeping (3) left out; the run's three steps fold into the newest (5).
	want := []float64{0, 1, 5, 6, 7}
	if len(seqs) != len(want) {
		t.Fatalf("seqs = %v, want %v", seqs, want)
	}
	for i := range want {
		if seqs[i] != want[i] {
			t.Fatalf("seqs = %v, want %v (oldest first)", seqs, want)
		}
	}
	first := rows[0].(map[string]any)
	if first["kind"] != "status" || first["glyph"] != "▲" || first["tone"] != "critical" || first["who"] != "Datadog" ||
		first["text"] != "5xx over 2% for 5 minutes" || first["at"] != "2026-10-08T15:52:00Z" {
		t.Fatalf("trigger row: %v", first)
	}
	step := rows[2].(map[string]any)
	if step["kind"] != "beacon" || step["text"] != "origin-b in us-east-1c" || step["detail"] != "step 3, latest of 3 steps" || step["who"] != "Beacon" {
		t.Fatalf("folded step: %v", step)
	}
	claim := rows[3].(map[string]any)
	if claim["kind"] != "findings" || claim["who"] != "bob · Claude Code" || claim["text"] != "5xx starts at 15:45Z" {
		t.Fatalf("claim row: %v", claim)
	}
	// Read with the room's own session, as a person: no agent instance named.
	for _, c := range f.callsTo(eventsPath) {
		if c.Token != roomToken || strings.Contains(c.Query, "agentInstanceId") {
			t.Fatalf("call %+v", c)
		}
	}
}

func TestTimelineKindPagesBackUntilItHasEnough(t *testing.T) {
	f := newFakeLandfall(t)
	// 600 rows; a finding every 100th.
	f.serveEvents(eventsPath, makeEvents(600, func(seq int) map[string]any {
		if seq%100 == 50 {
			return map[string]any{"type": "claim.staged", "payload": map[string]any{"statement": "finding"}}
		}
		return map[string]any{"type": "chat.message", "payload": map[string]any{"text": "chatter"}}
	}))
	ans := roundTrip(t, RunTimeline(context.Background(), TimelineOptions{Before: -1, Limit: 3, Kind: "findings"}, f.deps(true)))
	rows := ans["events"].([]any)
	if len(rows) != 3 || ans["hasMore"] != true {
		t.Fatalf("answer: %v", ans)
	}
	for i, want := range []float64{350, 450, 550} {
		if got := rows[i].(map[string]any)["seq"]; got != want {
			t.Fatalf("row %d seq %v, want %v", i, got, want)
		}
	}
	calls := f.callsTo(eventsPath)
	if len(calls) < 2 || !strings.Contains(calls[1].Query, "beforeSeq=") {
		t.Fatalf("expected it to page backwards: %+v", calls)
	}
	if ans["oldestSeq"] != float64(350) {
		t.Fatalf("oldestSeq = %v", ans["oldestSeq"])
	}

	// From an explicit --before, down to the start: nothing more after that.
	ans = roundTrip(t, RunTimeline(context.Background(), TimelineOptions{Before: 200, Limit: 10, Kind: "findings"}, f.deps(true)))
	rows = ans["events"].([]any)
	if len(rows) != 2 || ans["hasMore"] != false {
		t.Fatalf("answer: %v", ans)
	}
}

func TestTimelineStopsAfterFivePagesAndSaysThereMayBeMore(t *testing.T) {
	f := newFakeLandfall(t)
	f.serveEvents(eventsPath, makeEvents(3000, func(int) map[string]any {
		return map[string]any{"type": "chat.message", "payload": map[string]any{"text": "chatter"}}
	}))
	ans := RunTimeline(context.Background(), TimelineOptions{Before: -1, Limit: 5, Kind: "beacon"}, f.deps(true))
	if n := len(f.callsTo(eventsPath)); n != timelinePagesMax {
		t.Fatalf("%d pages read, want the cap of %d", n, timelinePagesMax)
	}
	if ans["hasMore"] != true || len(ans["events"].([]map[string]any)) != 0 {
		t.Fatalf("answer: %v", ans)
	}
}

func TestTimelineErrors(t *testing.T) {
	ctx := context.Background()
	wantFailure(t, RunTimeline(ctx, TimelineOptions{Before: -1}, noRoomDeps()), msgNoRoom)

	f := newFakeLandfall(t)
	wantFailure(t, RunTimeline(ctx, TimelineOptions{Before: -1, Kind: "gossip"}, f.deps(true)), "Pick a kind: findings, status, beacon, people or all.")

	f.status("GET "+eventsPath, http.StatusUnauthorized)
	wantFailure(t, RunTimeline(ctx, TimelineOptions{Before: -1}, f.deps(true)), msgRoomExpired)

	f.status("GET "+eventsPath, http.StatusForbidden)
	wantFailure(t, RunTimeline(ctx, TimelineOptions{Before: -1}, f.deps(true)), "Landfall refused to show the timeline to this room's session.")

	f.handle("GET "+eventsPath, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"not":"a list"}`)) })
	wantFailure(t, RunTimeline(ctx, TimelineOptions{Before: -1}, f.deps(true)), "Landfall sent a timeline this CLI could not read.")

	wantFailure(t, RunTimeline(ctx, TimelineOptions{Room: "rk9", Before: -1}, f.deps(true)), "No room rk9 is open on this machine. Join it first.")
}

func TestEventKindBuckets(t *testing.T) {
	for typ, want := range map[string]string{
		"claim.admitted": "findings", "edge.finding": "findings", "agent.finding": "findings",
		"incident.triggered": "status", "status.changed": "status", "severity.changed": "status", "stage.changed": "status",
		"agent.run.started": "beacon", "agent.step": "beacon", "agent.run.concluded": "beacon",
		"edge.participant.joined": "people", "chat.message": "people",
		"artifact.shared": "other",
	} {
		if got := eventKind(typ); got != want {
			t.Fatalf("%s → %s, want %s", typ, got, want)
		}
	}
}

// A refused signal read must not show the server's reason code as its text.
func TestTimelineRefusedSignalReadHasAPlainSentence(t *testing.T) {
	f := newFakeLandfall(t)
	f.serveEvents(eventsPath, []map[string]any{
		{"seq": 0, "type": "edge.signals.denied", "actorType": "system", "payload": map[string]any{"reason": "no-live-grant", "source": "cloudwatch", "operation": "listLoadBalancers"}},
	})
	ans := roundTrip(t, RunTimeline(context.Background(), TimelineOptions{Before: -1, Limit: 50, Kind: "all"}, f.deps(true)))
	rows := ans["events"].([]any)
	if len(rows) != 1 {
		t.Fatalf("answer: %v", ans)
	}
	row := rows[0].(map[string]any)
	if row["text"] != "a signal read was refused" || row["kind"] != "other" || strings.Contains(row["text"].(string), "no-live-grant") {
		t.Fatalf("row: %v", row)
	}
}
