package cli

import (
	"context"
	"net/http"
	"testing"
)

const widgetsDataPath = "/o/acme/incidents/inc-1/widgets/data"

// wallEvents is a canvas: a query-backed chart, a data-only stat, a failed
// widget, a query-backed log view, and a shared arrangement.
func wallEvents() []map[string]any {
	return []map[string]any{
		{"seq": 1, "type": "agent.widget.requested", "payload": map[string]any{"widgetId": "w1", "title": "5xx error rate", "widgetType": "chart"}},
		{"seq": 2, "type": "agent.widget.validated", "payload": map[string]any{"widgetId": "w1", "moduleId": "mod-1", "type": "chart"}},
		{"seq": 3, "type": "agent.widget.requested", "payload": map[string]any{"widgetId": "w2", "title": "Healthy origins", "widgetType": "stat"}},
		{"seq": 4, "type": "agent.widget.executed", "payload": map[string]any{"widgetId": "w2", "type": "stat", "data": map[string]any{"value": 4, "unit": "/6", "tone": "warning", "spark": []any{6, 6, 5, 4}}}},
		{"seq": 5, "type": "agent.widget.requested", "payload": map[string]any{"widgetId": "w3", "title": "broken"}},
		{"seq": 6, "type": "agent.widget.failed", "payload": map[string]any{"widgetId": "w3", "error": "boom"}},
		{"seq": 7, "type": "agent.widget.requested", "payload": map[string]any{"widgetId": "w4", "title": "Logs · web-edge", "widgetType": "logView"}},
		{"seq": 8, "type": "agent.widget.validated", "payload": map[string]any{"widgetId": "w4", "moduleId": "mod-4", "type": "logView"}},
		{"seq": 9, "type": "canvas.layout.saved", "payload": map[string]any{"scope": "sub:alice", "layout": map[string]any{"v": 1, "order": []any{"w4"}, "size": map[string]any{}}, "displayName": "alice"}},
		{"seq": 10, "type": "canvas.layout.saved", "payload": map[string]any{"scope": "shared", "layout": map[string]any{"v": 1, "order": []any{"w2", "w1", "gone"}, "size": map[string]any{}}, "displayName": "carol"}},
	}
}

func TestWallReadsTheSharedArrangementAsThePerson(t *testing.T) {
	f := newFakeLandfall(t)
	f.serveEvents(eventsPath, wallEvents())
	f.handle("POST "+widgetsDataPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("authorization") != "Bearer "+personToken {
			t.Errorf("the wall's data must be read as the person, got %q", r.Header.Get("authorization"))
			w.WriteHeader(http.StatusForbidden)
			return
		}
		writeJSON200(w, map[string]any{
			"window":    map[string]any{"from": "2026-10-08T10:00:00Z", "to": "2026-10-08T16:00:00Z", "label": "incident"},
			"fetchedAt": "2026-10-08T16:00:00Z",
			"widgets": map[string]any{
				"w1": map[string]any{"widgetId": "w1", "status": "rendered", "data": map[string]any{
					"series":     []any{map[string]any{"label": "5xxErrorRate", "unit": "%", "points": []any{map[string]any{"t": "2026-10-08T15:00:00Z", "v": 0.2}, map[string]any{"t": "2026-10-08T15:01:00Z", "v": 2.4}}}},
					"markers":    []any{map[string]any{"t": "2026-10-08T15:45:00Z", "label": "web-edge v2.3.1"}},
					"thresholds": []any{map[string]any{"value": 2, "tone": "critical"}},
				}},
				"w4": map[string]any{"widgetId": "w4", "status": "failed", "error": map[string]any{"kind": "credential", "message": "needs a connection you cannot read"}},
			},
		})
	})
	ans := roundTrip(t, RunWall(context.Background(), "rk1", f.deps(true)))
	if ans["ok"] != true || ans["sharedBy"] != "carol" || ans["windowMs"] != float64(6*3600*1000) {
		t.Fatalf("answer: %v", ans)
	}
	widgets := ans["widgets"].([]any)
	if len(widgets) != 2 {
		t.Fatalf("widgets: %v", widgets)
	}
	stat, chart := widgets[0].(map[string]any), widgets[1].(map[string]any)
	if stat["id"] != "w2" || stat["value"] != "4" || stat["unit"] != "/6" || stat["tone"] != "warning" || len(stat["spark"].([]any)) != 4 {
		t.Fatalf("stat (first, as shared): %v", stat)
	}
	if chart["id"] != "w1" || chart["threshold"] != float64(2) || chart["tone"] != "critical" {
		t.Fatalf("chart: %v", chart)
	}
	pts := chart["series"].([]any)[0].(map[string]any)["points"].([]any)
	if first := pts[0].([]any); first[0] != float64(1791471600000) || first[1] != 0.2 {
		t.Fatalf("points: %v", pts)
	}
	if m := chart["markers"].([]any)[0].(map[string]any); m["atMs"] != float64(1791474300000) {
		t.Fatalf("markers: %v", m)
	}
	unavailable := ans["unavailable"].([]any)
	if len(unavailable) != 1 || unavailable[0].(map[string]any)["id"] != "w4" || unavailable[0].(map[string]any)["reason"] != "needs a connection you cannot read" {
		t.Fatalf("unavailable: %v", unavailable)
	}
	// One batch, only the query-backed widgets, in wall order.
	calls := f.callsTo(widgetsDataPath)
	if len(calls) != 1 {
		t.Fatalf("calls: %+v", calls)
	}
	ids := calls[0].Body["widgetIds"].([]any)
	if len(ids) != 2 || ids[0] != "w1" || ids[1] != "w4" {
		t.Fatalf("widgetIds = %v", ids)
	}
}

func TestWallWithoutSignInNeverReadsAsSomebodyElse(t *testing.T) {
	f := newFakeLandfall(t)
	f.serveEvents(eventsPath, wallEvents())
	f.status("POST "+widgetsDataPath, http.StatusOK)
	ans := roundTrip(t, RunWall(context.Background(), "", f.deps(false)))
	if len(f.callsTo(widgetsDataPath)) != 0 {
		t.Fatal("without the person's sign-in the data must not be read with the room's session")
	}
	widgets, unavailable := ans["widgets"].([]any), ans["unavailable"].([]any)
	if len(widgets) != 1 || widgets[0].(map[string]any)["id"] != "w2" {
		t.Fatalf("the data-only stat still draws: %v", widgets)
	}
	if len(unavailable) != 2 || unavailable[0].(map[string]any)["reason"] != "Sign in to read this widget as yourself: run landfall login." {
		t.Fatalf("unavailable: %v", unavailable)
	}
}

func TestWallSignedInElsewhere(t *testing.T) {
	f := newFakeLandfall(t)
	f.serveEvents(eventsPath, wallEvents())
	other := testRoom
	other.Slug = "globex"
	other.Readers = testRoom.Readers
	d := f.deps(true, other)
	d.RoomRead = f.deps(true).RoomRead // the same room's events, read as acme's fixture
	ans := RunWall(context.Background(), "rk1", d)
	u := ans["unavailable"].([]map[string]any)
	if len(u) != 2 || u[0]["reason"] != "You are signed in to another organization, so this widget cannot be read as you." {
		t.Fatalf("unavailable: %v", u)
	}
}

func TestWallErrors(t *testing.T) {
	ctx := context.Background()
	wantFailure(t, RunWall(ctx, "", noRoomDeps()), msgNoRoom)

	f := newFakeLandfall(t)
	f.status("GET "+eventsPath, http.StatusForbidden)
	wantFailure(t, RunWall(ctx, "", f.deps(true)), "Landfall refused to show the room's canvas to this room's session.")

	f.serveEvents(eventsPath, wallEvents())
	f.status("POST "+widgetsDataPath, http.StatusUnauthorized)
	ans := RunWall(ctx, "", f.deps(true))
	if u := ans["unavailable"].([]map[string]any); len(u) != 2 || u[0]["reason"] != msgLoginExpired {
		t.Fatalf("an expired sign-in marks the query-backed widgets: %v", ans)
	}
}

func TestFlattenWidgetTypes(t *testing.T) {
	table := flattenWidget("t", "table", "T", map[string]any{
		"columns": []any{map[string]any{"key": "host", "label": "Host"}, map[string]any{"key": "err", "label": "Errors"}},
		"rows":    []any{map[string]any{"host": "a", "err": map[string]any{"v": 3.0, "tone": "critical"}}},
	})
	if rows := table["rows"].([][]string); rows[0][0] != "a" || rows[0][1] != "3" || table["columns"].([]string)[1] != "Errors" {
		t.Fatalf("table: %v", table)
	}
	graph := flattenWidget("g", "graph", "Topology", map[string]any{
		"nodes": []any{map[string]any{"id": "cf", "label": "cloudfront", "tone": "warning"}},
		"edges": []any{map[string]any{"from": "cf", "to": "alb", "trust": "confirmed"}},
	})
	if graph["nodes"].([]map[string]any)[0]["tone"] != "warning" || graph["edges"].([]map[string]any)[0]["trust"] != "confirmed" {
		t.Fatalf("graph: %v", graph)
	}
	events := flattenWidget("e", "events", "Audit", map[string]any{"events": []any{map[string]any{
		"t": "2026-10-08T15:00:00Z", "actor": map[string]any{"kind": "user", "id": "arn:x", "label": "deploy-bot"}, "action": "UpdateService",
		"target": map[string]any{"kind": "ecs.service", "id": "web-edge"}, "outcome": "success",
	}}})
	if ev := events["events"].([]map[string]any)[0]; ev["actor"] != "deploy-bot" || ev["target"] != "web-edge" || ev["outcome"] != "success" {
		t.Fatalf("events: %v", ev)
	}
	tl := flattenWidget("tl", "timeline", "When", map[string]any{"events": []any{map[string]any{"t": "2026-10-08T15:00:00Z", "label": "deploy", "tone": "warning"}}})
	if it := tl["items"].([]map[string]any)[0]; it["label"] != "deploy" || it["tone"] != "warning" {
		t.Fatalf("timeline: %v", it)
	}
	logs := flattenWidget("l", "logView", "Logs", map[string]any{"lines": []any{map[string]any{"level": "error", "message": "upstream timeout pool=origin-b"}}})
	if l := logs["lines"].([]map[string]any)[0]; l["text"] != "upstream timeout pool=origin-b" || l["level"] != "error" {
		t.Fatalf("logView: %v", l)
	}
	unknown := flattenWidget("u", "hologram", "Future", map[string]any{"x": 1})
	if len(unknown) != 3 || unknown["type"] != "hologram" {
		t.Fatalf("an unknown type passes through with type and title only: %v", unknown)
	}
	if empty := flattenWidget("n", "chart", "No data", nil); empty["empty"] != true {
		t.Fatalf("no data: %v", empty)
	}
}
