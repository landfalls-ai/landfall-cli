package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

func TestShareWithRoomFromQueryChartsTheReadItself(t *testing.T) {
	fc := &fakeClient{querySignalsResult: client.SignalsQueryResult{
		"source": "cloudwatch", "operation": "getMetricData",
		"raw": map[string]any{"MetricDataResults": []any{map[string]any{
			"Label": "DbCpuUtilization", "Timestamps": []any{"2026-10-06T17:00:00Z", "2026-10-06T17:01:00Z"}, "Values": []any{11.9, 96.5},
		}}},
	}}
	acc := &fakeAccepter{}
	tool := shareTool(t, newSession(fc), acc)
	_, err := tool.Handler(context.Background(), map[string]any{
		"text":      "Database CPU over the incident",
		"fromQuery": map[string]any{"source": "cloudwatch", "operation": "getMetricData", "params": map[string]any{"MetricName": "DbCpuUtilization"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if fc.lastQuery.source != "cloudwatch" || fc.lastQuery.params["MetricName"] != "DbCpuUtilization" {
		t.Fatalf("the read was not made as given: %+v", fc.lastQuery)
	}
	if acc.widget == nil || acc.widget.WidgetType != "chart" || acc.widget.Title != "DbCpuUtilization" {
		t.Fatalf("widget = %+v, want a chart titled by the series", acc.widget)
	}
	if acc.lastKind != "widget" {
		t.Fatalf("kind = %q, want widget", acc.lastKind)
	}
	pts := acc.widget.Data["series"].([]any)[0].(map[string]any)["points"].([]any)
	if len(pts) != 2 {
		t.Fatalf("got %d points, want both", len(pts))
	}
}

func TestShareWithRoomFromQueryRefusesAReadWithNoSeries(t *testing.T) {
	fc := &fakeClient{querySignalsResult: client.SignalsQueryResult{"source": "cloudwatch", "operation": "filterLogEvents",
		"raw": map[string]any{"events": []any{map[string]any{"message": "boom"}}}}}
	acc := &fakeAccepter{}
	tool := shareTool(t, newSession(fc), acc)
	_, err := tool.Handler(context.Background(), map[string]any{
		"text":      "logs",
		"fromQuery": map[string]any{"source": "cloudwatch", "operation": "filterLogEvents"},
	})
	if err == nil || !strings.Contains(err.Error(), "no time series") {
		t.Fatalf("err = %v, want a refusal that says why", err)
	}
	if acc.text != "" {
		t.Fatal("nothing may be queued when the chart cannot be built")
	}
}
