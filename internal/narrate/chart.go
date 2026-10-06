package narrate

// chart.go — a signal read as a chart widget's data.
//
// An agent that wanted a metric on the room's canvas used to copy the points
// out of a query_signals answer into share_with_room's `widget.data` by hand:
// slow, token-heavy, and wrong whenever it rounded or dropped a point. This
// turns the read itself into the chart's {series:[{label, unit?, points:[{t, v}]}]},
// using the same readSeries the summary view uses, so every source shape the
// CLI can read (CloudWatch, Datadog, Prometheus, GCP, Azure, mcp-grafana) can
// also be charted. share_with_room's `fromQuery` and the Claude Code mod's
// one-key "add this chart to the room" both come through here.

import (
	"encoding/json"
	"fmt"
	"time"
)

// ChartMaxSeries bounds how many series one chart carries; ChartMaxPoints how
// many points each keeps (evenly thinned past that, first and last kept).
const (
	ChartMaxSeries = 6
	ChartMaxPoints = 240
)

// ChartFromSignalRead reads a query_signals result as chart data. ok is false
// when the read holds no time series with at least one point (logs, a list, an
// error), and nothing should be charted.
func ChartFromSignalRead(result map[string]any) (data map[string]any, title string, ok bool) {
	payload, hasPayload := result["raw"]
	if !hasPayload && !isSignalEnvelope(result) {
		payload, hasPayload = any(result), true
	}
	if !hasPayload || payload == nil {
		return nil, "", false
	}
	views, isSeries := readSeries(payload)
	if !isSeries {
		// An MCP server's tool answers in text, very often JSON text: read it
		// as a payload once, as the summary view does.
		if text, isText := mcpText(payload); isText {
			var inner any
			if json.Unmarshal([]byte(text), &inner) == nil {
				views, isSeries = readSeries(inner)
			}
		}
	}
	if !isSeries {
		return nil, "", false
	}
	series := make([]any, 0, len(views))
	labels := []string{}
	for _, v := range views {
		if len(v.points) == 0 {
			continue
		}
		if len(series) == ChartMaxSeries {
			break
		}
		pts := thin(v.points, ChartMaxPoints)
		out := make([]any, 0, len(pts))
		for _, p := range pts {
			out = append(out, map[string]any{"t": p.t.UTC().Format(time.RFC3339), "v": p.v})
		}
		s := map[string]any{"label": v.label, "points": out}
		if v.unit != "" {
			s["unit"] = v.unit
		}
		series = append(series, s)
		labels = append(labels, v.label)
	}
	if len(series) == 0 {
		return nil, "", false
	}
	title = labels[0]
	if len(labels) > 1 {
		title = fmt.Sprintf("%s and %d more", labels[0], len(labels)-1)
	}
	return map[string]any{"series": series}, title, true
}

// thin keeps at most n points, evenly spaced, always keeping the first and
// the last so the chart spans the whole window it was read for.
func thin(pts []point, n int) []point {
	if len(pts) <= n || n < 2 {
		return pts
	}
	out := make([]point, 0, n)
	step := float64(len(pts)-1) / float64(n-1)
	for i := 0; i < n; i++ {
		out = append(out, pts[int(float64(i)*step+0.5)])
	}
	return out
}
