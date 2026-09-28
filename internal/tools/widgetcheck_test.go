package tools

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// catalogExamples reads the room's own example payload for every widget type.
func catalogExamples(t *testing.T) map[string]map[string]any {
	t.Helper()
	raw, err := os.ReadFile("testdata/widget-catalog-examples.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var all map[string]any
	if err := json.Unmarshal(raw, &all); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	out := map[string]map[string]any{}
	for k, v := range all {
		if m, ok := v.(map[string]any); ok {
			out[k] = m
		}
	}
	return out
}

// TestEveryCatalogExampleIsAccepted: the room renders its own examples, so a
// local check that refused one would be refusing a widget the room accepts.
// The fixture also has to cover every type this CLI advertises.
func TestEveryCatalogExampleIsAccepted(t *testing.T) {
	examples := catalogExamples(t)
	for _, wt := range widgetTypes {
		name := wt.(string)
		data, ok := examples[name]
		if !ok {
			t.Errorf("no catalog example for %q: add it to testdata when the type is added", name)
			continue
		}
		if problems := ValidateWidget(&WidgetPayload{WidgetType: name, Title: "Example", Data: data}); len(problems) != 0 {
			t.Errorf("%s example refused: %v", name, problems)
		}
	}
}

// chartWith builds a one-series chart around the given points.
func chartWith(points ...any) map[string]any {
	return map[string]any{"series": []any{map[string]any{"label": "p99", "points": points}}}
}

func point(t, v any) map[string]any { return map[string]any{"t": t, "v": v} }

func TestTheCheckNamesEachProblemPrecisely(t *testing.T) {
	manySeries := []any{}
	for i := 0; i < maxWidgetSeries+1; i++ {
		manySeries = append(manySeries, map[string]any{"label": "s", "points": []any{}})
	}
	manyPoints := []any{}
	for i := 0; i < maxWidgetPoints+1; i++ {
		manyPoints = append(manyPoints, point("2026-09-28T10:00:00Z", 1.0))
	}
	cases := []struct {
		name string
		w    *WidgetPayload
		want string
	}{
		{"unknown type", &WidgetPayload{WidgetType: "pie", Title: "x", Data: map[string]any{}}, `unknown widget type "pie" (known: stat, chart,`},
		{"no type", &WidgetPayload{Title: "x", Data: map[string]any{}}, "widget.widgetType is required"},
		{"no title", &WidgetPayload{WidgetType: "stat", Data: map[string]any{"value": 1.0}}, "widget.title is required"},
		{"no data", &WidgetPayload{WidgetType: "stat", Title: "x"}, "widget.data must be an object"},
		{"stat without value", &WidgetPayload{WidgetType: "stat", Title: "x", Data: map[string]any{"unit": "%"}}, "stat.value must be a finite number (got nothing)"},
		{"stat value as text", &WidgetPayload{WidgetType: "stat", Title: "x", Data: map[string]any{"value": "12"}}, `stat.value must be a finite number (got "12")`},
		{"chart without series", &WidgetPayload{WidgetType: "chart", Title: "x", Data: map[string]any{}}, "chart.series must be an array (got nothing)"},
		{"t not ISO", &WidgetPayload{WidgetType: "chart", Title: "x", Data: chartWith(point("14:05", 1.0))}, `chart.series[0].points[0].t must be an ISO-8601 time such as 2026-09-28T14:05:00Z (got "14:05")`},
		{"t a number", &WidgetPayload{WidgetType: "chart", Title: "x", Data: chartWith(point(1727517900000.0, 1.0))}, "chart.series[0].points[0].t must be an ISO-8601 time"},
		{"t missing", &WidgetPayload{WidgetType: "chart", Title: "x", Data: chartWith(map[string]any{"v": 1.0})}, "chart.series[0].points[0].t is required"},
		{"v as text", &WidgetPayload{WidgetType: "chart", Title: "x", Data: chartWith(point("2026-09-28T14:05:00Z", "3.5"))}, `chart.series[0].points[0].v must be a finite number (got "3.5")`},
		{"v null", &WidgetPayload{WidgetType: "chart", Title: "x", Data: chartWith(point("2026-09-28T14:05:00Z", nil))}, "chart.series[0].points[0].v must be a finite number (got nothing)"},
		{"unknown point key", &WidgetPayload{WidgetType: "chart", Title: "x", Data: chartWith(map[string]any{"t": "2026-09-28T14:05:00Z", "v": 1.0, "value": 1.0})}, `chart.series[0].points[0]: unexpected key "value" (allowed: t, v)`},
		{"too many series", &WidgetPayload{WidgetType: "chart", Title: "x", Data: map[string]any{"series": manySeries}}, "chart.series holds 9 series; the cap is 8"},
		{"too many points", &WidgetPayload{WidgetType: "chart", Title: "x", Data: chartWith(manyPoints...)}, "chart.series[0].points holds 5001 points; the cap is 5000"},
		{"unknown top-level key", &WidgetPayload{WidgetType: "stat", Title: "x", Data: map[string]any{"value": 1.0, "color": "red"}}, `stat: unexpected key "color"`},
		{"bad tone", &WidgetPayload{WidgetType: "stat", Title: "x", Data: map[string]any{"value": 1.0, "tone": "red"}}, `stat.tone must be one of good, warning, serious, critical, neutral (got "red")`},
		{"table bad column type", &WidgetPayload{WidgetType: "table", Title: "x", Data: map[string]any{"columns": []any{map[string]any{"key": "a", "label": "A", "type": "text"}}, "rows": []any{}}}, `table.columns[0].type must be one of string, number, time, status, share (got "text")`},
		{"table bad cell", &WidgetPayload{WidgetType: "table", Title: "x", Data: map[string]any{"columns": []any{}, "rows": []any{map[string]any{"a": []any{1.0}}}}}, "table.rows[0].a must be a string, a number, or {v, tone?}"},
		{"timeline t", &WidgetPayload{WidgetType: "timeline", Title: "x", Data: map[string]any{"events": []any{map[string]any{"t": "yesterday", "label": "deploy"}}}}, "timeline.events[0].t must be an ISO-8601 time"},
		{"geo place", &WidgetPayload{WidgetType: "geo", Title: "x", Data: map[string]any{"points": []any{map[string]any{"value": 1.0}}}}, "geo.points[0].place must be a non-empty string"},
		{"geo lat", &WidgetPayload{WidgetType: "geo", Title: "x", Data: map[string]any{"points": []any{map[string]any{"place": "DUB", "lat": 91.0}}}}, "geo.points[0].lat must be a number from -90 to 90"},
		{"graph dangling edge", &WidgetPayload{WidgetType: "graph", Title: "x", Data: map[string]any{"nodes": []any{map[string]any{"id": "a", "label": "a"}}, "edges": []any{map[string]any{"from": "a", "to": "b"}}}}, `graph.edges[0].to names no node in graph.nodes (got "b")`},
		{"graph duplicate id", &WidgetPayload{WidgetType: "graph", Title: "x", Data: map[string]any{"nodes": []any{map[string]any{"id": "a", "label": "a"}, map[string]any{"id": "a", "label": "b"}}, "edges": []any{}}}, `graph.nodes[1].id "a" is not unique`},
		{"events actor kind", &WidgetPayload{WidgetType: "events", Title: "x", Data: map[string]any{"events": []any{map[string]any{"t": "2026-09-21T03:12:44Z", "action": "Put", "actor": map[string]any{"kind": "IAM User", "id": "u"}}}}}, "events.events[0].actor.kind must be a lower-case token"},
		{"codeFinding permalink", &WidgetPayload{WidgetType: "codeFinding", Title: "x", Data: map[string]any{"codeRef": map[string]any{"owner": "o", "repo": "r", "ref": "main", "permalink": "http://x"}, "snippet": map[string]any{"lines": []any{"a"}, "startLine": 1.0}}}, "codeFinding.codeRef.permalink must be an https URL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			problems := ValidateWidget(tc.w)
			for _, p := range problems {
				if strings.Contains(p, tc.want) {
					return
				}
			}
			t.Fatalf("problems %q do not include %q", problems, tc.want)
		})
	}
}

func TestISOTimesTheRendererCanPlace(t *testing.T) {
	for _, ok := range []string{
		"2026-09-28T14:05:00Z", "2026-09-28T14:05:00.123Z", "2026-09-28T14:05:00+03:00",
		"2026-09-28T14:05:00+0300", "2026-09-28T14:05:00", "2026-09-28T14:05Z", "2026-09-28T14:05",
		"2026-09-28", "2026-09-28 14:05:00Z",
	} {
		if !isISOTime(ok) {
			t.Errorf("isISOTime(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "14:05", "yesterday", "1727517900", "28/09/2026 14:05", "2026-13-01T00:00:00Z", "Sep 28 2026"} {
		if isISOTime(bad) {
			t.Errorf("isISOTime(%q) = true, want false", bad)
		}
	}
}

// TestTheSameProblemsReportInTheSameOrder: an agent retrying with one fix
// must see the rest unchanged, not reshuffled by map iteration.
func TestTheSameProblemsReportInTheSameOrder(t *testing.T) {
	w := &WidgetPayload{WidgetType: "stat", Title: "x", Data: map[string]any{"a": 1.0, "b": 1.0, "c": 1.0, "d": 1.0}}
	first := strings.Join(ValidateWidget(w), "|")
	for i := 0; i < 20; i++ {
		if again := strings.Join(ValidateWidget(w), "|"); again != first {
			t.Fatalf("order changed:\n%s\n%s", first, again)
		}
	}
}

// --- through the tools ------------------------------------------------------

// TestShareRefusesABadWidgetInTheSameResult: the whole point. The agent hears
// what to fix now, and nothing is queued to fail later where it cannot see.
func TestShareRefusesABadWidgetInTheSameResult(t *testing.T) {
	acc := &fakeAccepter{}
	tool := shareTool(t, newSession(&fakeClient{}), acc)
	_, err := tool.Handler(context.Background(), map[string]any{
		"text": "p99 latency by region",
		"widget": map[string]any{
			"widgetType": "chart",
			"title":      "p99 latency",
			"data":       chartWith(point("14:05", 812.0), point("2026-09-28T14:06:00Z", "901")),
		},
	})
	if err == nil {
		t.Fatal("a chart the room would refuse was queued")
	}
	msg := err.Error()
	for _, want := range []string{
		`Nothing was shared: the "chart" widget does not match the shape the room renders.`,
		`chart.series[0].points[0].t must be an ISO-8601 time`,
		`chart.series[0].points[1].v must be a finite number (got "901")`,
		"Fix these and share it again.",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error lacks %q:\n%s", want, msg)
		}
	}
	if acc.calls != 0 {
		t.Fatalf("the hand-off was queued anyway (%d)", acc.calls)
	}
}

func TestShareQueuesAGoodWidget(t *testing.T) {
	acc := &fakeAccepter{}
	tool := shareTool(t, newSession(&fakeClient{}), acc)
	out, err := tool.Handler(context.Background(), map[string]any{
		"text":   "p99 latency by region",
		"widget": map[string]any{"widgetType": "chart", "title": "p99 latency", "data": chartWith(point("2026-09-28T14:05:00Z", 812.0))},
	})
	if err != nil {
		t.Fatalf("share: %v", err)
	}
	if acc.calls != 1 || acc.widget == nil || !strings.HasPrefix(out, "shared") {
		t.Fatalf("calls=%d widget=%v out=%q", acc.calls, acc.widget, out)
	}
}

func TestAWidgetKindWithoutAWidgetIsRefused(t *testing.T) {
	acc := &fakeAccepter{}
	tool := shareTool(t, newSession(&fakeClient{}), acc)
	_, err := tool.Handler(context.Background(), map[string]any{"text": "chart: errors by minute", "kind": "widget"})
	if err == nil || !strings.Contains(err.Error(), "needs `widget`") {
		t.Fatalf("err = %v", err)
	}
	if acc.calls != 0 {
		t.Fatal("an empty widget was queued")
	}
}

func TestAWidgetThatIsNotAnObjectIsRefused(t *testing.T) {
	acc := &fakeAccepter{}
	tool := shareTool(t, newSession(&fakeClient{}), acc)
	_, err := tool.Handler(context.Background(), map[string]any{"text": "x", "widget": `{"widgetType":"stat"}`})
	if err == nil || !strings.Contains(err.Error(), "`widget` must be an object") {
		t.Fatalf("err = %v", err)
	}
	if acc.calls != 0 {
		t.Fatal("queued anyway")
	}
}

// TestPostWidgetRefusesBeforeItPosts: in direct mode the wrapper's post is
// best-effort, so a refused widget used to read "added" to the agent.
func TestPostWidgetRefusesBeforeItPosts(t *testing.T) {
	c := &fakeClient{}
	list := Build(newSession(c))
	_, err := find(t, list, "post_widget").Handler(context.Background(), map[string]any{
		"widgetType": "stat", "title": "Error rate", "data": map[string]any{"value": "high"},
	})
	if err == nil || !strings.Contains(err.Error(), "Nothing was posted") || !strings.Contains(err.Error(), "post it again") {
		t.Fatalf("err = %v", err)
	}
	if n := len(c.recorded("contribute")); n != 0 {
		t.Fatalf("the bad widget was posted (%d)", n)
	}

	out := callTool(t, list, "post_widget", map[string]any{"widgetType": "stat", "title": "Error rate", "data": map[string]any{"value": 6.7}})
	if !strings.Contains(out, `Widget "Error rate" added`) || len(c.recorded("contribute")) != 1 {
		t.Fatalf("a good widget did not post: %q", out)
	}
}
