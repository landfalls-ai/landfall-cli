package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/landfalls-ai/landfall-cli/internal/spool"
)

func metricRead() map[string]any {
	return map[string]any{
		"source": "cloudwatch", "operation": "getMetricData",
		"raw": map[string]any{"MetricDataResults": []any{map[string]any{
			"Label": "Global", "Timestamps": []any{"2026-10-06T17:00:00Z", "2026-10-06T17:01:00Z"}, "Values": []any{0.2, 6.1},
		}}},
	}
}

func TestRunChartQueuesAChartFromTheRoomsOwnRead(t *testing.T) {
	var queued *spool.WidgetPayload
	var queuedText, queuedIncident, queuedInstance string
	var asked chartQuery
	title, err := RunChart(chartQuery{Source: "cloudwatch", Operation: "getMetricData", Params: map[string]any{"MetricName": "5xxErrorRate"}, Title: "5xxErrorRate"}, ChartDeps{
		Room: func() (string, string, error) { return "room-1", "inc-1", nil },
		Query: func(roomKey string, q chartQuery) (map[string]any, string, error) {
			asked = q
			return metricRead(), "agent-7", nil
		},
		Queue: func(incidentID, instance, text string, w *spool.WidgetPayload) error {
			queuedIncident, queuedInstance, queuedText, queued = incidentID, instance, text, w
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if asked.Params["MetricName"] != "5xxErrorRate" {
		t.Fatalf("the read was not made as given: %+v", asked)
	}
	if title != "5xxErrorRate" || queued == nil || queued.WidgetType != "chart" || queued.Title != "5xxErrorRate" {
		t.Fatalf("title %q, queued %+v", title, queued)
	}
	if queuedIncident != "inc-1" || queuedInstance != "agent-7" || queuedText != "Chart: 5xxErrorRate" {
		t.Fatalf("queued for %q as %q with %q", queuedIncident, queuedInstance, queuedText)
	}
}

func TestRunChartRefusesAReadWithNoSeriesAndQueuesNothing(t *testing.T) {
	queued := false
	_, err := RunChart(chartQuery{Source: "cloudwatch", Operation: "filterLogEvents"}, ChartDeps{
		Room: func() (string, string, error) { return "room-1", "inc-1", nil },
		Query: func(string, chartQuery) (map[string]any, string, error) {
			return map[string]any{"raw": map[string]any{"events": []any{map[string]any{"message": "boom"}}}}, "a", nil
		},
		Queue: func(string, string, string, *spool.WidgetPayload) error { queued = true; return nil },
	})
	if err == nil || !strings.Contains(err.Error(), "no time series") || queued {
		t.Fatalf("err=%v queued=%v", err, queued)
	}
}

func TestRunChartSaysWhyWhenNoRoomOrNoQuery(t *testing.T) {
	if _, err := RunChart(chartQuery{Operation: "x"}, ChartDeps{}); err == nil {
		t.Fatal("a query without a source must be refused before anything else")
	}
	_, err := RunChart(chartQuery{Source: "s", Operation: "o"}, ChartDeps{
		Room: func() (string, string, error) {
			return "", "", errors.New("this checkout is not reading any room right now")
		},
	})
	if err == nil || !strings.Contains(err.Error(), "not reading any room") {
		t.Fatalf("err = %v", err)
	}
}
