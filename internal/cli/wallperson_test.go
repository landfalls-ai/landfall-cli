package cli

import (
	"context"
	"errors"
	"testing"
)

// edgeRow is an edge.* row as edge-consolidation writes it.
func edgeRow(seq int, typ, who, name string, payload map[string]any) map[string]any {
	p := map[string]any{"humanActorId": who, "agentInstanceId": "ai-" + who, "displayName": name, "kind": "member", "at": "2026-10-08T15:00:00Z"}
	if who == "h-alice" {
		p["edgeAgentLabel"] = "Codex"
	}
	for k, v := range payload {
		p[k] = v
	}
	return map[string]any{"seq": seq, "type": typ, "payload": p, "occurredAt": "2026-10-08T15:00:00Z"}
}

func claimRow(seq int, typ, who string, payload map[string]any) map[string]any {
	p := map[string]any{"humanActorId": who, "agentInstanceId": "ai-" + who, "displayName": who}
	for k, v := range payload {
		p[k] = v
	}
	return map[string]any{"seq": seq, "type": typ, "payload": p}
}

// personEvents is a room where alice built a dashboard and a trail, bob built
// one widget, and carol only voted.
func personEvents() []map[string]any {
	stat := func(v float64) map[string]any { return map[string]any{"value": v, "unit": "%", "tone": "critical"} }
	return []map[string]any{
		{"seq": 0, "type": "incident.opened", "payload": map[string]any{}},
		edgeRow(1, "edge.participant.joined", "h-alice", "alice", nil),
		edgeRow(2, "edge.widget", "h-alice", "alice", map[string]any{"widgetType": "stat", "title": "Origin pool saturation", "data": stat(81), "capturedAt": "2026-10-08T15:02:00Z"}),
		edgeRow(4, "edge.widget", "h-alice", "alice", map[string]any{"widgetType": "geo", "title": "Pool connections by zone", "data": map[string]any{"points": []any{map[string]any{"place": "us-east-1a", "value": 98}}}}),
		edgeRow(5, "edge.widget", "h-alice", "alice", map[string]any{"widgetType": "stat", "title": "Nothing yet", "data": map[string]any{}}),
		edgeRow(6, "edge.widget", "h-alice", "alice", map[string]any{"widgetType": "stat", "title": "Origin pool saturation", "data": stat(94), "capturedAt": "2026-10-08T15:06:00Z"}),
		edgeRow(7, "edge.widget", "h-bob", "bob", map[string]any{"widgetType": "stat", "title": "Origin pool saturation", "data": stat(50)}),
		{"seq": 8, "type": "canvas.layout.saved", "payload": map[string]any{"scope": "sub:h-alice", "layout": map[string]any{"v": 1, "order": []any{"edge-widget-6"}}}},
		{"seq": 9, "type": "canvas.layout.saved", "payload": map[string]any{"scope": "sub:h-alice", "layout": map[string]any{"v": 1, "order": []any{"edge-widget-4", "edge-widget-6", "gone"}}}},
		{"seq": 9, "type": "canvas.layout.saved", "payload": map[string]any{"scope": "shared", "layout": map[string]any{"v": 1, "order": []any{"x"}}}},
		// A gated finding, corroborated by bob, contested by carol.
		edgeRow(10, "edge.finding", "h-alice", "alice", map[string]any{"text": "Origin pool exhausted in us-east-1", "gated": true}),
		claimRow(11, "claim.staged", "h-alice", map[string]any{"statement": "Origin pool exhausted in us-east-1", "provenance": []any{map[string]any{"sourceType": "finding", "sourceSeq": 10}}}),
		claimRow(12, "claim.corroborated", "h-bob", map[string]any{"claimSeq": 11, "position": "corroborate"}),
		claimRow(13, "claim.contested", "h-carol", map[string]any{"claimSeq": 11, "position": "contest"}),
		edgeRow(14, "edge.finding", "h-alice", "alice", map[string]any{"text": "checking the pool limit", "contributionKind": "note"}),
		// A hypothesis that was admitted.
		edgeRow(15, "edge.hypothesis", "h-alice", "alice", map[string]any{"statement": "The pool limit was lowered in the 15:40 deploy"}),
		claimRow(16, "claim.staged", "h-alice", map[string]any{"provenance": []any{map[string]any{"sourceType": "hypothesis", "sourceSeq": 15}}}),
		claimRow(17, "claim.admitted", "h-carol", map[string]any{"claimSeq": 16}),
		edgeRow(18, "edge.query", "h-alice", "alice", map[string]any{"source": "cloudwatch", "operation": "metrics", "resource": "origin-pool"}),
		edgeRow(19, "edge.action.proposed", "h-alice", "alice", map[string]any{"description": "Raise the pool limit back to 200"}),
		// Only the author's own side agreed: still staged.
		edgeRow(20, "edge.hypothesis", "h-alice", "alice", map[string]any{"statement": "A retry storm made it worse"}),
		claimRow(21, "claim.staged", "h-alice", map[string]any{"provenance": []any{map[string]any{"sourceType": "hypothesis", "sourceSeq": 20}}}),
		claimRow(22, "claim.corroborated", "h-alice", map[string]any{"claimSeq": 21, "position": "corroborate"}),
		shared(23, "a1", "origin-pool-notes.md", "text/markdown", 4096, "sha-1", "h-alice", "alice", "member", "Codex"),
		shared(24, "a2", "same.md", "text/markdown", 4096, "sha-1", "h-bob", "bob", "member", ""),
		shared(25, "a3", "again.md", "text/markdown", 4096, "sha-1", "h-alice", "alice", "member", "Codex"),
	}
}

func TestWallPersonFoldsTheirDashboardTrailAndArtifacts(t *testing.T) {
	f := newFakeLandfall(t)
	f.serveEvents(eventsPath, personEvents())
	ans := roundTrip(t, RunWallWith(context.Background(), WallOptions{Room: "rk1", Person: "h-alice"}, f.deps(false)))
	if ans["ok"] != true {
		t.Fatalf("answer: %v", ans)
	}
	person := ans["person"].(map[string]any)
	if person["humanActorId"] != "h-alice" || person["displayName"] != "alice" || person["edgeAgentLabel"] != "Codex" || person["you"] != false {
		t.Fatalf("person: %v", person)
	}

	// Widgets: the empty snapshot left out, the refreshed stat one card with
	// its older version, in the order of the latest sub:h-alice layout.
	widgets := ans["widgets"].([]any)
	if len(widgets) != 2 {
		t.Fatalf("widgets: %v", widgets)
	}
	geo, stat := widgets[0].(map[string]any), widgets[1].(map[string]any)
	if geo["id"] != "edge-widget-4" || geo["type"] != "geo" || geo["points"].([]any)[0].(map[string]any)["place"] != "us-east-1a" {
		t.Fatalf("geo (first, as arranged): %v", geo)
	}
	if geo["capturedAt"] != "2026-10-08T15:00:00Z" {
		t.Fatalf("a snapshot without capturedAt takes its row's time: %v", geo["capturedAt"])
	}
	if stat["id"] != "edge-widget-6" || stat["value"] != "94" || stat["tone"] != "critical" || stat["capturedAt"] != "2026-10-08T15:06:00Z" || stat["seq"] != float64(6) {
		t.Fatalf("stat: %v", stat)
	}
	versions := stat["versions"].([]any)
	if len(versions) != 1 || versions[0].(map[string]any)["seq"] != float64(2) || versions[0].(map[string]any)["capturedAt"] != "2026-10-08T15:02:00Z" {
		t.Fatalf("versions: %v", versions)
	}
	if _, has := geo["versions"]; has {
		t.Fatal("a widget never refreshed has no versions")
	}
	if ans["arranged"] != true || len(ans["unavailable"].([]any)) != 0 {
		t.Fatalf("arranged %v unavailable %v", ans["arranged"], ans["unavailable"])
	}

	// Trail: newest first, a claim's state where it went through the gate.
	trail := ans["trail"].([]any)
	type row struct {
		seq               float64
		kind, text, state string
	}
	want := []row{
		{20, "hypothesis", "A retry storm made it worse", "staged"},
		{19, "suggestion", "Raise the pool limit back to 200", ""},
		{18, "query", "cloudwatch/metrics · origin-pool", ""},
		{15, "hypothesis", "The pool limit was lowered in the 15:40 deploy", "admitted"},
		{14, "note", "checking the pool limit", ""},
		{10, "finding", "Origin pool exhausted in us-east-1", "contested"},
	}
	if len(trail) != len(want) {
		t.Fatalf("trail: %v", trail)
	}
	for i, w := range want {
		r := trail[i].(map[string]any)
		state, _ := r["state"].(string)
		if r["seq"] != w.seq || r["kind"] != w.kind || r["text"] != w.text || state != w.state || r["at"] != "2026-10-08T15:00:00Z" {
			t.Fatalf("trail[%d] = %v, want %+v", i, r, w)
		}
	}
	if trail[5].(map[string]any)["type"] != "edge.finding" || trail[4].(map[string]any)["type"] != "edge.finding" {
		t.Fatalf("type is the server's event type: %v", trail)
	}

	// Artifacts: hers only, identical bytes collapsed (bob's copy is his).
	arts := ans["artifacts"].([]any)
	if len(arts) != 1 || arts[0].(map[string]any)["artifactId"] != "a1" {
		t.Fatalf("artifacts: %v", arts)
	}
}

func TestWallPeopleOnEveryAnswer(t *testing.T) {
	f := newFakeLandfall(t)
	f.serveEvents(eventsPath, personEvents())
	for _, o := range []WallOptions{{Room: "rk1"}, {Room: "rk1", Person: "h-bob"}} {
		ans := roundTrip(t, RunWallWith(context.Background(), o, f.deps(false)))
		people := ans["people"].([]any)
		if len(people) != 2 {
			t.Fatalf("people (%+v): %v", o, people)
		}
		alice, bob := people[0].(map[string]any), people[1].(map[string]any)
		if alice["humanActorId"] != "h-alice" || alice["displayName"] != "alice" || alice["edgeAgentLabel"] != "Codex" || alice["kind"] != "member" ||
			alice["widgets"] != float64(2) || alice["trail"] != float64(6) || alice["artifacts"] != float64(1) || alice["latestSeq"] != float64(25) {
			t.Fatalf("alice (newest activity first): %v", alice)
		}
		if bob["humanActorId"] != "h-bob" || bob["widgets"] != float64(1) || bob["trail"] != float64(0) || bob["artifacts"] != float64(1) || bob["latestSeq"] != float64(24) {
			t.Fatalf("bob: %v", bob)
		}
	}
}

func TestWallPersonMe(t *testing.T) {
	f := newFakeLandfall(t)
	f.serveEvents(eventsPath, personEvents())
	asked := ""
	me := func(_ context.Context, roomKey string) (string, error) { asked = roomKey; return "h-alice", nil }
	ans := roundTrip(t, RunWallWith(context.Background(), WallOptions{Room: "rk1", Person: "me", Me: me}, f.deps(false)))
	if asked != "rk1" {
		t.Fatalf("me asked for room %q", asked)
	}
	person := ans["person"].(map[string]any)
	if ans["ok"] != true || person["humanActorId"] != "h-alice" || person["you"] != true || len(ans["widgets"].([]any)) != 2 {
		t.Fatalf("me: %v", ans)
	}
	// Naming yourself by id reads the same, marked you.
	ans = roundTrip(t, RunWallWith(context.Background(), WallOptions{Room: "rk1", Person: "h-alice", Me: me}, f.deps(false)))
	if ans["person"].(map[string]any)["you"] != true {
		t.Fatalf("by id: %v", ans["person"])
	}

	const unknown = "Landfall does not know who you are in this room. Join it again with a fresh share link."
	wantFailure(t, RunWallWith(context.Background(), WallOptions{Room: "rk1", Person: "me"}, f.deps(false)), unknown)
	broken := func(context.Context, string) (string, error) { return "", errors.New("no session") }
	wantFailure(t, RunWallWith(context.Background(), WallOptions{Room: "rk1", Person: "me", Me: broken}, f.deps(false)), unknown)
	blank := func(context.Context, string) (string, error) { return "", nil }
	wantFailure(t, RunWallWith(context.Background(), WallOptions{Room: "rk1", Person: "ME", Me: blank}, f.deps(false)), unknown)
}

func TestWallPersonWithNothingAndBuildOrder(t *testing.T) {
	f := newFakeLandfall(t)
	events := personEvents()
	// Without a sub:h-alice layout the cards come in build order: each by its
	// latest version's seq (the refreshed stat moves after the geo).
	var kept []map[string]any
	for _, e := range events {
		if e["type"] != "canvas.layout.saved" {
			kept = append(kept, e)
		}
	}
	f.serveEvents(eventsPath, kept)
	ans := roundTrip(t, RunWallWith(context.Background(), WallOptions{Room: "rk1", Person: "h-alice"}, f.deps(false)))
	widgets := ans["widgets"].([]any)
	if ans["arranged"] != false || len(widgets) != 2 || widgets[0].(map[string]any)["id"] != "edge-widget-4" || widgets[1].(map[string]any)["id"] != "edge-widget-6" {
		t.Fatalf("build order: %v", widgets)
	}

	ans = roundTrip(t, RunWallWith(context.Background(), WallOptions{Room: "rk1", Person: "h-dave"}, f.deps(false)))
	if ans["ok"] != true || len(ans["widgets"].([]any)) != 0 || len(ans["trail"].([]any)) != 0 || len(ans["artifacts"].([]any)) != 0 {
		t.Fatalf("a person with nothing: %v", ans)
	}
	if p := ans["person"].(map[string]any); p["humanActorId"] != "h-dave" || p["displayName"] != "" {
		t.Fatalf("person: %v", p)
	}
	// The shared wall still never reads edge widgets.
	ans = roundTrip(t, RunWall(context.Background(), "rk1", f.deps(false)))
	if len(ans["widgets"].([]any)) != 0 || len(ans["people"].([]any)) != 2 {
		t.Fatalf("shared wall: %v", ans)
	}
}
