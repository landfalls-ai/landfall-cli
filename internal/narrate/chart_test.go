package narrate

import (
	"testing"
)

func TestChartFromACloudWatchGetMetricDataRead(t *testing.T) {
	result := map[string]any{
		"source": "cloudwatch", "operation": "getMetricData",
		"raw": map[string]any{"MetricDataResults": []any{
			map[string]any{
				"Id": "cpu", "Label": "DbCpuUtilization",
				// Newest first, as CloudWatch answers: the chart must come out in time order.
				"Timestamps": []any{"2026-10-06T17:02:00Z", "2026-10-06T17:01:00Z", "2026-10-06T17:00:00Z"},
				"Values":     []any{96.5, 54.6, 11.9},
			},
			map[string]any{"Id": "empty", "Label": "NoData", "Timestamps": []any{}, "Values": []any{}},
		}},
	}
	data, title, ok := ChartFromSignalRead(result)
	if !ok {
		t.Fatal("a GetMetricData read with points must chart")
	}
	if title != "DbCpuUtilization" {
		t.Fatalf("title = %q, want the series' own name", title)
	}
	series := data["series"].([]any)
	if len(series) != 1 {
		t.Fatalf("got %d series, want the empty one left out", len(series))
	}
	pts := series[0].(map[string]any)["points"].([]any)
	first := pts[0].(map[string]any)
	if first["t"] != "2026-10-06T17:00:00Z" || first["v"] != 11.9 {
		t.Fatalf("first point = %v, want the oldest", first)
	}
}

func TestChartRefusesALogRead(t *testing.T) {
	result := map[string]any{"source": "cloudwatch", "operation": "filterLogEvents",
		"raw": map[string]any{"events": []any{map[string]any{"timestamp": 1.0, "message": "boom"}}}}
	if _, _, ok := ChartFromSignalRead(result); ok {
		t.Fatal("a log read has no series to chart")
	}
}

func TestChartThinsLongSeriesKeepingBothEnds(t *testing.T) {
	ts, vs := []any{}, []any{}
	for i := 0; i < 1000; i++ {
		ts = append(ts, "2026-10-06T00:00:00Z")
		vs = append(vs, float64(i))
	}
	// distinct times so the ends are identifiable
	for i := range ts {
		ts[i] = timeAt(i)
	}
	result := map[string]any{"raw": map[string]any{"MetricDataResults": []any{
		map[string]any{"Label": "x", "Timestamps": ts, "Values": vs},
	}}}
	data, _, ok := ChartFromSignalRead(result)
	if !ok {
		t.Fatal("expected a chart")
	}
	pts := data["series"].([]any)[0].(map[string]any)["points"].([]any)
	if len(pts) != ChartMaxPoints {
		t.Fatalf("kept %d points, want %d", len(pts), ChartMaxPoints)
	}
	if pts[0].(map[string]any)["v"] != 0.0 || pts[len(pts)-1].(map[string]any)["v"] != 999.0 {
		t.Fatal("thinning must keep the first and the last point")
	}
}

func TestChartTitlesSeveralSeries(t *testing.T) {
	result := map[string]any{"raw": map[string]any{"MetricDataResults": []any{
		map[string]any{"Label": "cpu", "Timestamps": []any{timeAt(0)}, "Values": []any{1.0}},
		map[string]any{"Label": "hit rate", "Timestamps": []any{timeAt(0)}, "Values": []any{2.0}},
	}}}
	_, title, _ := ChartFromSignalRead(result)
	if title != "cpu and 1 more" {
		t.Fatalf("title = %q", title)
	}
}

func timeAt(i int) string {
	h, m := i/60, i%60
	return "2026-10-06T" + two(h%24) + ":" + two(m) + ":00Z"
}

func two(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}
