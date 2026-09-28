package narrate

// signals.go: query_signals' answer, as something an agent reads.
//
// The route answers with the provider's own response inside a thin envelope
// ({source, operation, params, fetchedAt, raw, partial?, error?}, the
// server's RawResult). The tool used to print that envelope indented, with no
// bound: one CloudWatch log-group read is 100+ KB, nearly all of it JSON
// punctuation and fields no agent reads, spent out of the agent's context on
// a single call.
//
// This renders the shapes the connected sources are known to answer with, the
// same ones the server's own source plugins shape for widgets (each plugin's
// toEntries / toSeries): log reads as a count, a
// time range and "HH:MM:SSZ message" lines; metric reads as each series' name,
// point count, min, max and last value, and its points. Anything it does not
// recognize is rendered generically (the largest list in the answer, one item
// per line). Every view is bounded, and says when it trimmed and how to get
// more. The provider's raw envelope is one argument away (raw: true), capped
// too, because an unbounded dump is the failure this file exists to end.
//
// Parsed leniently and never guessed: a payload is read as logs or metrics
// only when at least one of its items actually reads that way, so an answer
// that merely shares a field name with a log payload falls through to the
// generic view rather than being reported as "0 log events".

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// SignalLinesDefault is how many log lines (or metric points, or list
	// items) a read shows when the agent does not say.
	SignalLinesDefault = 40
	// SignalLinesMax is the most maxLines can ask for.
	SignalLinesMax = 200
	// SignalRawMaxBytes caps raw: true, and the rendered view as a backstop.
	SignalRawMaxBytes = 64 * 1024

	// signalLineMax bounds one rendered line (a log message, a list item).
	// A line cut to fit is counted and the view says so (see lineCuts).
	signalLineMax = 300
	// signalNameMax bounds one series name.
	signalNameMax = 160
	// signalSeriesMax bounds how many series get a summary line.
	signalSeriesMax = 50
	// signalObjectMax bounds a payload with no list in it, rendered as JSON.
	signalObjectMax = 8 * 1024
	// signalTokenMax is the longest page token printed inline.
	signalTokenMax = 1024
)

// SignalReadOptions is what the agent asked for on top of the read itself.
type SignalReadOptions struct {
	// MaxLines is the line budget; <= 0 means SignalLinesDefault, and it is
	// clamped to SignalLinesMax.
	MaxLines int
	// Raw asks for the provider's envelope as JSON, capped at
	// SignalRawMaxBytes.
	Raw bool
}

func (o SignalReadOptions) lines() int {
	switch {
	case o.MaxLines <= 0:
		return SignalLinesDefault
	case o.MaxLines > SignalLinesMax:
		return SignalLinesMax
	default:
		return o.MaxLines
	}
}

// RenderSignalRead renders one query_signals answer. source and operation
// name the read when the envelope does not.
func RenderSignalRead(source, operation string, result map[string]any, opts SignalReadOptions) string {
	if s, _ := result["source"].(string); s != "" {
		source = s
	}
	if s, _ := result["operation"].(string); s != "" {
		operation = s
	}
	head := strings.TrimSpace(source + " " + operation)
	if head == "" {
		head = "The read"
	}
	if len(result) == 0 {
		return head + ": the source returned no data."
	}
	if opts.Raw {
		return renderRawEnvelope(result)
	}

	// An envelope carries the payload in `raw`, absent when the source failed.
	// A map with none of the envelope's own fields is a payload as it stands
	// (an older server, a remote source that answers bare).
	payload, hasPayload := result["raw"]
	if !hasPayload && !isSignalEnvelope(result) {
		payload, hasPayload = any(result), true
	}

	empty := !hasPayload || payload == nil || isEmptyValue(payload)

	var sb strings.Builder
	partial, _ := result["partial"].(bool)
	errText, _ := result["error"].(string)
	if partial || strings.TrimSpace(errText) != "" {
		reason := oneLine(errText, 400)
		if reason == "" {
			reason = "it gave no reason"
		}
		fmt.Fprintf(&sb, "%s did not fully answer: %s. The read was allowed; the source itself failed or degraded, "+
			"so retry, narrow the request, or try another operation.", head, reason)
		if empty {
			return sb.String()
		}
		sb.WriteString("\n\nWhat it did return:\n")
	}
	if empty {
		return head + ": the source returned no data."
	}

	sb.WriteString(renderSignalPayload(head, payload, opts.lines(), 0))
	return capView(sb.String())
}

func isEmptyValue(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		return len(t) == 0
	case []any:
		return len(t) == 0
	case string:
		return strings.TrimSpace(t) == ""
	}
	return false
}

func isSignalEnvelope(m map[string]any) bool {
	for _, k := range []string{"source", "operation", "fetchedAt", "partial", "error", "params"} {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}

// renderSignalPayload picks the view for a payload: logs, metrics, an MCP
// tool's text, plain text, or the generic list view.
func renderSignalPayload(head string, payload any, n, depth int) string {
	if entries, total, ok := readLogEntries(payload); ok {
		return renderLogs(head, entries, total, n, pageToken(payload))
	}
	if series, ok := readSeries(payload); ok {
		return renderSeries(head, series, n)
	}
	if text, ok := mcpText(payload); ok {
		// An MCP server's tool answers in text, very often JSON text: read it
		// again as a payload once, so a Loki answer relayed through an MCP
		// server still reads as log lines.
		var inner any
		if depth == 0 && json.Unmarshal([]byte(strings.TrimSpace(text)), &inner) == nil {
			if _, isObj := inner.(map[string]any); isObj {
				return renderSignalPayload(head, inner, n, depth+1)
			}
			if _, isArr := inner.([]any); isArr {
				return renderSignalPayload(head, inner, n, depth+1)
			}
		}
		return renderText(head, text, n)
	}
	if s, ok := payload.(string); ok {
		return renderText(head, s, n)
	}
	return renderGeneric(head, payload, n)
}

// --- logs -------------------------------------------------------------------

type logLine struct {
	t      time.Time
	line   string
	stream string
}

// readLogEntries mirrors the server plugins' toEntries. ok is true when the
// payload is a log answer: a recognized container with no items (nothing
// matched), or one where at least one item reads as a log line. total is the
// container's item count, so lines that could not be read are reported rather
// than silently missing.
func readLogEntries(payload any) ([]logLine, int, bool) {
	m, isMap := payload.(map[string]any)
	if !isMap {
		return nil, 0, false
	}
	for _, read := range []func(map[string]any) ([]logLine, int, bool){
		cloudwatchLogs, datadogLogs, lokiStreams, coralogixLogs, gcpLogs, azureLogs,
	} {
		if entries, total, ok := read(m); ok && (len(entries) > 0 || total == 0) {
			sort.SliceStable(entries, func(i, j int) bool { return entries[i].t.Before(entries[j].t) })
			return entries, total, true
		}
	}
	return nil, 0, false
}

// cloudwatchLogs: FilterLogEvents / GetLogEvents, events[].{timestamp,message,logStreamName}.
func cloudwatchLogs(m map[string]any) ([]logLine, int, bool) {
	items, ok := m["events"].([]any)
	if !ok {
		return nil, 0, false
	}
	var out []logLine
	for _, it := range items {
		e, _ := it.(map[string]any)
		t, tok := signalTime(e["timestamp"])
		msg, mok := e["message"].(string)
		if !tok || !mok {
			continue
		}
		stream, _ := e["logStreamName"].(string)
		out = append(out, logLine{t: t, line: msg, stream: stream})
	}
	return out, len(items), true
}

// datadogLogs: v1 logs[].content or v2 data[].attributes, each {timestamp,message,service}.
func datadogLogs(m map[string]any) ([]logLine, int, bool) {
	items, ok := m["logs"].([]any)
	if !ok {
		if items, ok = m["data"].([]any); !ok {
			return nil, 0, false
		}
	}
	var out []logLine
	for _, it := range items {
		e, _ := it.(map[string]any)
		body, _ := e["content"].(map[string]any)
		if body == nil {
			body, _ = e["attributes"].(map[string]any)
		}
		t, tok := signalTime(body["timestamp"])
		msg, mok := body["message"].(string)
		if !tok || !mok {
			continue
		}
		service, _ := body["service"].(string)
		out = append(out, logLine{t: t, line: msg, stream: service})
	}
	return out, len(items), true
}

// lokiStreams: a query_range streams answer, data.result[].{stream, values: [[ns, line], ...]}.
func lokiStreams(m map[string]any) ([]logLine, int, bool) {
	data, _ := m["data"].(map[string]any)
	result, ok := data["result"].([]any)
	if !ok {
		return nil, 0, false
	}
	isStreams := data["resultType"] == "streams"
	if !isStreams {
		for _, x := range result {
			r, _ := x.(map[string]any)
			if _, s := r["stream"].(map[string]any); s {
				if _, v := r["values"].([]any); v {
					isStreams = true
					break
				}
			}
		}
	}
	if !isStreams {
		return nil, 0, false
	}
	var out []logLine
	total := 0
	for _, x := range result {
		r, _ := x.(map[string]any)
		values, _ := r["values"].([]any)
		stream := promLabel(r["stream"])
		for _, row := range values {
			total++
			pair, _ := row.([]any)
			if len(pair) < 2 {
				continue
			}
			t, tok := signalTime(pair[0])
			line, lok := pair[1].(string)
			if !tok || !lok {
				continue
			}
			out = append(out, logLine{t: t, line: line, stream: stream})
		}
	}
	return out, total, true
}

// coralogixLogs: a DataPrime batch, result.results[] (or results[]), each with
// metadata/labels as [{key,value}] and a JSON userData.
func coralogixLogs(m map[string]any) ([]logLine, int, bool) {
	container := m
	if inner, ok := m["result"].(map[string]any); ok {
		container = inner
	}
	items, ok := container["results"].([]any)
	if !ok {
		return nil, 0, false
	}
	var out []logLine
	for _, it := range items {
		e, _ := it.(map[string]any)
		meta := keyValues(e["metadata"])
		labels := keyValues(e["labels"])
		var user map[string]any
		if s, isStr := e["userData"].(string); isStr {
			_ = json.Unmarshal([]byte(s), &user)
		} else if u, isObj := e["userData"].(map[string]any); isObj {
			user = u
		}
		stamp := any(meta["timestamp"])
		if meta["timestamp"] == "" {
			stamp = user["timestamp"]
		}
		t, tok := signalTime(stamp)
		if !tok {
			continue
		}
		line := ""
		if user != nil {
			for _, k := range []string{"message", "log", "msg", "text"} {
				if s, isStr := user[k].(string); isStr {
					line = s
					break
				}
			}
			if line == "" {
				b, _ := json.Marshal(user)
				line = string(b)
			}
		} else if s, isStr := e["userData"].(string); isStr {
			line = s
		} else {
			continue
		}
		stream := labels["applicationname"]
		if stream == "" {
			stream = labels["subsystemname"]
		}
		out = append(out, logLine{t: t, line: line, stream: stream})
	}
	return out, len(items), true
}

func keyValues(v any) map[string]string {
	out := map[string]string{}
	list, _ := v.([]any)
	for _, it := range list {
		kv, _ := it.(map[string]any)
		k, kok := kv["key"].(string)
		if !kok {
			continue
		}
		switch val := kv["value"].(type) {
		case string:
			out[k] = val
		case float64:
			out[k] = strconv.FormatFloat(val, 'f', -1, 64)
		}
	}
	return out
}

// gcpLogs: Cloud Logging entries:list, entries[].{timestamp, textPayload | jsonPayload}.
func gcpLogs(m map[string]any) ([]logLine, int, bool) {
	items, ok := m["entries"].([]any)
	if !ok {
		return nil, 0, false
	}
	var out []logLine
	for _, it := range items {
		e, _ := it.(map[string]any)
		stamp, isStr := e["timestamp"].(string)
		if !isStr {
			continue
		}
		t, tok := signalTime(stamp)
		if !tok {
			continue
		}
		line, isText := e["textPayload"].(string)
		if !isText {
			if e["jsonPayload"] == nil {
				continue
			}
			b, _ := json.Marshal(e["jsonPayload"])
			line = string(b)
		}
		out = append(out, logLine{t: t, line: line})
	}
	return out, len(items), true
}

// azureLogs: a Log Analytics answer, tables[0].{columns, rows}, keyed by TimeGenerated.
func azureLogs(m map[string]any) ([]logLine, int, bool) {
	tables, _ := m["tables"].([]any)
	if len(tables) == 0 {
		return nil, 0, false
	}
	table, _ := tables[0].(map[string]any)
	columns, cok := table["columns"].([]any)
	rows, rok := table["rows"].([]any)
	if !cok || !rok {
		return nil, 0, false
	}
	names := make([]string, len(columns))
	timeIdx := -1
	for i, c := range columns {
		col, _ := c.(map[string]any)
		names[i], _ = col["name"].(string)
		if names[i] == "TimeGenerated" {
			timeIdx = i
		}
	}
	if timeIdx < 0 {
		return nil, 0, false
	}
	var out []logLine
	for _, r := range rows {
		row, _ := r.([]any)
		if timeIdx >= len(row) {
			continue
		}
		t, tok := signalTime(row[timeIdx])
		if !tok {
			continue
		}
		parts := make([]string, 0, len(names))
		for i, name := range names {
			if i == timeIdx || i >= len(row) {
				continue
			}
			parts = append(parts, name+"="+scalarText(row[i]))
		}
		out = append(out, logLine{t: t, line: strings.Join(parts, " ")})
	}
	return out, len(rows), true
}

func renderLogs(head string, entries []logLine, total, n int, token *pageTokenInfo) string {
	var sb strings.Builder
	if len(entries) == 0 {
		fmt.Fprintf(&sb, "%s: no log events came back.", head)
		if total > 0 {
			fmt.Fprintf(&sb, " %s in the answer could not be read as log lines; pass raw: true to see them.", plural(total, "item", "items"))
		}
		writeToken(&sb, token)
		return sb.String()
	}
	first, last := entries[0].t, entries[len(entries)-1].t
	multiDay := !sameDay(first, last)
	fmt.Fprintf(&sb, "%s: %s, %s", head, plural(len(entries), "log event", "log events"), timeRange(first, last))
	streams := map[string]bool{}
	for _, e := range entries {
		if e.stream != "" {
			streams[e.stream] = true
		}
	}
	if len(streams) > 1 {
		fmt.Fprintf(&sb, ", across %s", plural(len(streams), "stream", "streams"))
	}
	sb.WriteString(".\n")

	var cut lineCuts
	headN, tailN := split(len(entries), n)
	for _, e := range entries[:headN] {
		sb.WriteString(clock(e.t, multiDay) + " " + cut.line(e.line, signalLineMax) + "\n")
	}
	hidden := len(entries) - headN - tailN
	if hidden > 0 {
		fmt.Fprintf(&sb, "… %s not shown …\n", plural(hidden, "event", "events"))
		for _, e := range entries[len(entries)-tailN:] {
			sb.WriteString(clock(e.t, multiDay) + " " + cut.line(e.line, signalLineMax) + "\n")
		}
	}

	if hidden > 0 {
		shown := "the first " + strconv.Itoa(headN)
		if tailN > 0 {
			shown += " and the last " + strconv.Itoa(tailN)
		}
		fmt.Fprintf(&sb, "Trimmed: showing %s of %s. To see more, narrow the time window or the filter in params, "+
			"or raise maxLines (up to %d).", shown, plural(len(entries), "event", "events"), SignalLinesMax)
	}
	if skipped := total - len(entries); skipped > 0 {
		if hidden > 0 {
			sb.WriteString(" ")
		}
		fmt.Fprintf(&sb, "%s in the answer could not be read as log lines; pass raw: true to see them.",
			plural(skipped, "item", "items"))
	}
	writeNote(&sb, cut.note("line", "lines", signalLineMax))
	writeToken(&sb, token)
	return strings.TrimRight(sb.String(), "\n")
}

// --- metrics ----------------------------------------------------------------

type point struct {
	t time.Time
	v float64
}

type seriesView struct {
	label, unit string
	points      []point
}

// readSeries mirrors the server plugins' toSeries.
func readSeries(payload any) ([]seriesView, bool) {
	m, isMap := payload.(map[string]any)
	if !isMap {
		return nil, false
	}
	for _, read := range []func(map[string]any) ([]seriesView, bool){
		cloudwatchStatistics, cloudwatchMetricData, datadogSeries, promSeries, gcpSeries, azureSeries,
	} {
		if series, ok := read(m); ok {
			for i := range series {
				pts := series[i].points
				sort.SliceStable(pts, func(a, b int) bool { return pts[a].t.Before(pts[b].t) })
			}
			return series, true
		}
	}
	return nil, false
}

// cloudwatchStatistics: GetMetricStatistics, {Label, Datapoints[].{Timestamp, Value|Average|Sum|Maximum|Minimum, Unit}}.
func cloudwatchStatistics(m map[string]any) ([]seriesView, bool) {
	items, ok := m["Datapoints"].([]any)
	if !ok {
		return nil, false
	}
	s := seriesView{label: "series"}
	if l, _ := m["Label"].(string); l != "" {
		s.label = l
	}
	for _, it := range items {
		d, _ := it.(map[string]any)
		t, tok := signalTime(d["Timestamp"])
		v, vok := firstNumber(d, "Value", "Average", "Sum", "Maximum", "Minimum")
		if !tok || !vok {
			continue
		}
		if u, _ := d["Unit"].(string); u != "" && u != "None" && s.unit == "" {
			s.unit = u
		}
		s.points = append(s.points, point{t, v})
	}
	return []seriesView{s}, true
}

// cloudwatchMetricData: GetMetricData, MetricDataResults[].{Id, Label, Timestamps[], Values[]}.
func cloudwatchMetricData(m map[string]any) ([]seriesView, bool) {
	items, ok := m["MetricDataResults"].([]any)
	if !ok {
		return nil, false
	}
	var out []seriesView
	for _, it := range items {
		r, isMap := it.(map[string]any)
		if !isMap {
			continue
		}
		s := seriesView{label: firstString(r, "Label", "Id")}
		ts, _ := r["Timestamps"].([]any)
		vs, _ := r["Values"].([]any)
		for i, raw := range ts {
			if i >= len(vs) {
				break
			}
			t, tok := signalTime(raw)
			v, vok := finiteNumber(vs[i])
			if tok && vok {
				s.points = append(s.points, point{t, v})
			}
		}
		out = append(out, s)
	}
	return out, len(out) > 0
}

// datadogSeries: v1 queryMetrics, series[].{scope|metric|expression, pointlist: [[ms, v]], unit}.
func datadogSeries(m map[string]any) ([]seriesView, bool) {
	items, ok := m["series"].([]any)
	if !ok {
		return nil, false
	}
	var out []seriesView
	for _, it := range items {
		r, _ := it.(map[string]any)
		pl, isList := r["pointlist"].([]any)
		if !isList {
			continue
		}
		s := seriesView{label: firstString(r, "scope", "metric", "expression"), unit: datadogUnit(r["unit"])}
		for _, row := range pl {
			pair, _ := row.([]any)
			if len(pair) < 2 {
				continue
			}
			t, tok := signalTime(pair[0])
			v, vok := finiteNumber(pair[1])
			if tok && vok {
				s.points = append(s.points, point{t, v})
			}
		}
		out = append(out, s)
	}
	return out, len(out) > 0
}

// datadogUnit is a v1 series' unit, [{short_name, name, ...}, per_unit], by
// its short name.
func datadogUnit(v any) string {
	list, _ := v.([]any)
	for _, u := range list {
		unit, _ := u.(map[string]any)
		for _, k := range []string{"short_name", "name"} {
			if s, ok := unit[k].(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// promSeries: a Prometheus API answer, data.result[] as a matrix (values) or a
// vector (value). A Loki streams answer carries log lines and is never read here.
func promSeries(m map[string]any) ([]seriesView, bool) {
	data, _ := m["data"].(map[string]any)
	result, ok := data["result"].([]any)
	if !ok || data["resultType"] == "streams" {
		return nil, false
	}
	var out []seriesView
	for _, x := range result {
		r, _ := x.(map[string]any)
		var rows []any
		if vals, isList := r["values"].([]any); isList {
			rows = vals
		} else if val, isList := r["value"].([]any); isList {
			rows = []any{val}
		} else {
			continue
		}
		s := seriesView{label: promLabel(r["metric"])}
		for _, row := range rows {
			pair, _ := row.([]any)
			if len(pair) < 2 {
				continue
			}
			t, tok := signalTime(pair[0])
			v, vok := finiteNumber(pair[1])
			if tok && vok {
				s.points = append(s.points, point{t, v})
			}
		}
		out = append(out, s)
	}
	known := data["resultType"] == "matrix" || data["resultType"] == "vector"
	return out, len(out) > 0 || (known && len(result) == 0)
}

// gcpSeries: Cloud Monitoring timeSeries[].{metric.type, points[].{interval.endTime, value}}.
func gcpSeries(m map[string]any) ([]seriesView, bool) {
	items, ok := m["timeSeries"].([]any)
	if !ok {
		return nil, false
	}
	var out []seriesView
	for _, it := range items {
		r, _ := it.(map[string]any)
		pts, isList := r["points"].([]any)
		if !isList {
			continue
		}
		metric, _ := r["metric"].(map[string]any)
		s := seriesView{label: firstString(metric, "type")}
		for _, p := range pts {
			pm, _ := p.(map[string]any)
			interval, _ := pm["interval"].(map[string]any)
			value, _ := pm["value"].(map[string]any)
			t, tok := signalTime(interval["endTime"])
			v, vok := firstNumber(value, "doubleValue", "int64Value")
			if tok && vok {
				s.points = append(s.points, point{t, v})
			}
		}
		out = append(out, s)
	}
	return out, len(out) > 0
}

// azureSeries: Azure Monitor metrics, value[].{name.value, timeseries[].data[].{timeStamp, average|total|...}}.
func azureSeries(m map[string]any) ([]seriesView, bool) {
	items, ok := m["value"].([]any)
	if !ok {
		return nil, false
	}
	var out []seriesView
	for _, it := range items {
		r, _ := it.(map[string]any)
		tss, isList := r["timeseries"].([]any)
		if !isList {
			continue
		}
		name, _ := r["name"].(map[string]any)
		s := seriesView{label: firstString(name, "value")}
		if u, _ := r["unit"].(string); u != "" && u != "Unspecified" {
			s.unit = u
		}
		for _, ts := range tss {
			tm, _ := ts.(map[string]any)
			data, _ := tm["data"].([]any)
			for _, d := range data {
				dm, _ := d.(map[string]any)
				t, tok := signalTime(dm["timeStamp"])
				v, vok := firstNumber(dm, "average", "total", "count", "minimum", "maximum")
				if tok && vok {
					s.points = append(s.points, point{t, v})
				}
			}
		}
		out = append(out, s)
	}
	return out, len(out) > 0
}

func renderSeries(head string, series []seriesView, n int) string {
	if len(series) == 0 {
		return head + ": no series in this answer."
	}
	var sb strings.Builder
	totalPoints := 0
	var first, last time.Time
	for _, s := range series {
		totalPoints += len(s.points)
		if len(s.points) == 0 {
			continue
		}
		if first.IsZero() || s.points[0].t.Before(first) {
			first = s.points[0].t
		}
		if pl := s.points[len(s.points)-1].t; pl.After(last) {
			last = pl
		}
	}
	fmt.Fprintf(&sb, "%s: %s, %s", head, plural(len(series), "series", "series"), plural(totalPoints, "point", "points"))
	if totalPoints > 0 {
		sb.WriteString(", " + timeRange(first, last))
	}
	sb.WriteString(".\n")
	multiDay := totalPoints > 0 && !sameDay(first, last)

	shown := series
	if len(shown) > signalSeriesMax {
		shown = shown[:signalSeriesMax]
	}
	// The line budget is shared across the series shown, so a query that
	// returns thirty series does not print thirty full point lists. Fewer than
	// two points each lists none: the summary line carries min, max and last.
	per := n / len(shown)
	if per < 2 {
		per = 0
	}
	listed, withheld := 0, false
	var cut lineCuts
	for _, s := range shown {
		name := cut.line(s.label, signalNameMax)
		if s.unit != "" {
			name += " (" + s.unit + ")"
		}
		if len(s.points) == 0 {
			sb.WriteString(name + ": no points in this answer\n")
			continue
		}
		lo, hi := s.points[0].v, s.points[0].v
		for _, p := range s.points {
			lo, hi = math.Min(lo, p.v), math.Max(hi, p.v)
		}
		end := s.points[len(s.points)-1]
		fmt.Fprintf(&sb, "%s: %s; min %s, max %s, last %s at %s\n", name, plural(len(s.points), "point", "points"),
			number(lo), number(hi), number(end.v), clock(end.t, multiDay))
		if per == 0 {
			withheld = true
			continue
		}
		headN, tailN := split(len(s.points), per)
		for _, p := range s.points[:headN] {
			sb.WriteString("  " + clock(p.t, multiDay) + " " + number(p.v) + "\n")
		}
		listed += headN + tailN
		if hidden := len(s.points) - headN - tailN; hidden > 0 {
			withheld = true
			fmt.Fprintf(&sb, "  … %s not shown …\n", plural(hidden, "point", "points"))
			for _, p := range s.points[len(s.points)-tailN:] {
				sb.WriteString("  " + clock(p.t, multiDay) + " " + number(p.v) + "\n")
			}
		}
	}
	if more := len(series) - len(shown); more > 0 {
		fmt.Fprintf(&sb, "+%s not shown.\n", plural(more, "more series", "more series"))
	}
	switch {
	case per == 0 && totalPoints > 0:
		fmt.Fprintf(&sb, "Trimmed: points are not listed for %s sharing a %d-line budget; each summary above carries min, max and last. "+
			"To list points, query one series, or raise maxLines (up to %d).", plural(len(shown), "series", "series"), n, SignalLinesMax)
	case withheld || len(series) > len(shown):
		fmt.Fprintf(&sb, "Trimmed: showing %d of %s. To see more, narrow the time window, query fewer series, use a longer period (step) in params, "+
			"or raise maxLines (up to %d).", listed, plural(totalPoints, "point", "points"), SignalLinesMax)
	}
	writeNote(&sb, cut.note("series name", "series names", signalNameMax))
	return strings.TrimRight(sb.String(), "\n")
}

// --- text and everything else ------------------------------------------------

// mcpText is an MCP tool result's text, {content: [{type: "text", text}]},
// which is how an MCP-server source answers.
func mcpText(payload any) (string, bool) {
	m, _ := payload.(map[string]any)
	content, ok := m["content"].([]any)
	if !ok || len(content) == 0 {
		return "", false
	}
	var parts []string
	for _, c := range content {
		cm, _ := c.(map[string]any)
		if cm["type"] != "text" {
			continue
		}
		if s, isStr := cm["text"].(string); isStr {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, "\n"), true
}

func renderText(head, text string, n int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s: %s of text.\n", head, plural(len(lines), "line", "lines"))
	var cut lineCuts
	headN, tailN := split(len(lines), n)
	for _, l := range lines[:headN] {
		sb.WriteString(cut.text(l, signalLineMax) + "\n")
	}
	if hidden := len(lines) - headN - tailN; hidden > 0 {
		fmt.Fprintf(&sb, "… %s not shown …\n", plural(hidden, "line", "lines"))
		for _, l := range lines[len(lines)-tailN:] {
			sb.WriteString(cut.text(l, signalLineMax) + "\n")
		}
		fmt.Fprintf(&sb, "Trimmed: showing %d of %d lines. To see more, narrow the request in params, or raise maxLines (up to %d).",
			headN+tailN, len(lines), SignalLinesMax)
	}
	writeNote(&sb, cut.note("line", "lines", signalLineMax))
	return strings.TrimRight(sb.String(), "\n")
}

// renderGeneric shows an answer this file has no view for: its largest list,
// one item per line, then its other fields in brief.
func renderGeneric(head string, payload any, n int) string {
	var sb strings.Builder
	path, list := largestList(payload)
	m, _ := payload.(map[string]any)
	if list == nil {
		b := compactJSON(payload)
		if len(b) <= signalObjectMax {
			return head + ":\n" + b
		}
		return fmt.Sprintf("%s:\n%s\nTrimmed: the answer is %s and this shows the first %s. "+
			"Narrow the request in params, or pass raw: true for up to %s.",
			head, cutBytes(b, signalObjectMax), kb(len(b)), kb(signalObjectMax), kb(SignalRawMaxBytes))
	}

	where := path + " has"
	if path == "" {
		where = "the answer is a list of"
	}
	fmt.Fprintf(&sb, "%s: %s %s.\n", head, where, plural(len(list), "item", "items"))
	shown := list
	if len(shown) > n {
		shown = shown[:n]
	}
	var cut lineCuts
	for _, it := range shown {
		sb.WriteString(cut.line(scalarText(it), signalLineMax) + "\n")
	}

	// The other top-level fields, briefly: they often say what the list is
	// (a count, a window) or how to get the rest of it.
	token := pageToken(payload)
	if m != nil {
		keys := make([]string, 0, len(m))
		for k := range m {
			if k == path || strings.HasPrefix(path, k+".") || (token != nil && k == token.key) {
				continue
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if len(keys) > 12 {
			keys = keys[:12]
		}
		for _, k := range keys {
			v := m[k]
			if arr, isArr := v.([]any); isArr {
				fmt.Fprintf(&sb, "%s: %s\n", k, plural(len(arr), "item", "items"))
				continue
			}
			fmt.Fprintf(&sb, "%s: %s\n", k, cut.line(scalarText(v), signalLineMax))
		}
	}
	if more := len(list) - len(shown); more > 0 {
		fmt.Fprintf(&sb, "Trimmed: showing %d of %s. To see more, narrow the request in params, raise maxLines (up to %d), "+
			"or pass raw: true for the source's own answer (up to %s).", len(shown), plural(len(list), "item", "items"), SignalLinesMax, kb(SignalRawMaxBytes))
	}
	writeNote(&sb, cut.note("line", "lines", signalLineMax))
	writeToken(&sb, token)
	return strings.TrimRight(sb.String(), "\n")
}

// largestList finds the answer's main list: the payload itself, its largest
// top-level list, or the largest list one object down (data.result, say).
func largestList(payload any) (string, []any) {
	if arr, ok := payload.([]any); ok {
		return "", arr
	}
	m, ok := payload.(map[string]any)
	if !ok {
		return "", nil
	}
	best, bestPath := []any(nil), ""
	consider := func(path string, v any) {
		if arr, isArr := v.([]any); isArr && (best == nil || len(arr) > len(best) || (len(arr) == len(best) && path < bestPath)) {
			best, bestPath = arr, path
		}
	}
	for k, v := range m {
		consider(k, v)
	}
	if best != nil {
		return bestPath, best
	}
	for k, v := range m {
		if inner, isObj := v.(map[string]any); isObj {
			for ik, iv := range inner {
				consider(k+"."+ik, iv)
			}
		}
	}
	return bestPath, best
}

// --- paging -------------------------------------------------------------------

type pageTokenInfo struct{ key, value string }

// pageToken is a page token the source put at the top of its answer
// (CloudWatch's nextToken, the AWS SDK's NextToken): the read has more than
// this page, and the token is how to ask for the next one.
func pageToken(payload any) *pageTokenInfo {
	m, _ := payload.(map[string]any)
	for _, k := range []string{"nextToken", "NextToken"} {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return &pageTokenInfo{key: k, value: s}
		}
	}
	return nil
}

func writeToken(sb *strings.Builder, token *pageTokenInfo) {
	if token == nil {
		return
	}
	s := strings.TrimRight(sb.String(), "\n")
	sb.Reset()
	sb.WriteString(s)
	sb.WriteString("\n")
	if len(token.value) > signalTokenMax {
		fmt.Fprintf(sb, "The source has another page. Its %s is too long to show here; pass raw: true to read it, then repeat the call with it in params.", token.key)
		return
	}
	fmt.Fprintf(sb, "The source has another page: repeat the call with params.%s set to %s.", token.key, quoteText(token.value))
}

// --- cut lines ------------------------------------------------------------------

// lineCuts counts the lines a view cut to fit, so the view can say so. A line
// cut without notice reads as the whole message, and the end of a message is
// often the part that matters: a stack trace's exception, the last fields of a
// JSON log body.
type lineCuts struct{ n int }

// line is s folded onto one line and cut to max runes, counted when cut.
func (c *lineCuts) line(s string, max int) string {
	flat := strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(flat) <= max {
		return flat
	}
	c.n++
	return oneLine(flat, max)
}

// text is s as written, cut to max runes, counted when cut.
func (c *lineCuts) text(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	c.n++
	return truncateN(s, max)
}

// note is what the view says about its cuts, "" when it made none.
func (c *lineCuts) note(one, many string, max int) string {
	switch c.n {
	case 0:
		return ""
	case 1:
		return fmt.Sprintf("1 %s was cut at %d characters; pass raw: true to read it in full.", one, max)
	default:
		return fmt.Sprintf("%s were cut at %d characters; pass raw: true to read them in full.", plural(c.n, one, many), max)
	}
}

// writeNote puts a note on a line of its own after what sb already holds.
func writeNote(sb *strings.Builder, note string) {
	if note == "" {
		return
	}
	s := strings.TrimRight(sb.String(), "\n")
	sb.Reset()
	sb.WriteString(s)
	sb.WriteString("\n")
	sb.WriteString(note)
}

// --- raw ------------------------------------------------------------------------

func renderRawEnvelope(result map[string]any) string {
	full := compactJSON(result)
	if len(full) <= SignalRawMaxBytes {
		return full
	}
	return fmt.Sprintf("%s\n\nTrimmed: the raw answer is %s and this shows its first %s, so it is not valid JSON past this point. "+
		"Narrow the time window or the filter in params to get all of it, or leave raw off for a readable summary.",
		cutBytes(full, SignalRawMaxBytes), kb(len(full)), kb(SignalRawMaxBytes))
}

// capView is the rendered view's backstop: maxLines and the per-line bound
// already keep it far below this; a payload of enormous field values does not.
func capView(s string) string {
	if len(s) <= SignalRawMaxBytes {
		return s
	}
	cut := cutBytes(s, SignalRawMaxBytes)
	if i := strings.LastIndexByte(cut, '\n'); i > 0 {
		cut = cut[:i]
	}
	return cut + fmt.Sprintf("\nTrimmed at %s. Lower maxLines or narrow the request in params.", kb(SignalRawMaxBytes))
}

// --- small helpers ------------------------------------------------------------

// signalTime reads a time the way the server's epochToIso does: an epoch in
// seconds, milliseconds, microseconds or nanoseconds (Loki), told apart by
// magnitude, as a number or a numeric string, or an RFC 3339 string.
func signalTime(v any) (time.Time, bool) {
	switch t := v.(type) {
	case float64:
		return epochTime(t)
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return time.Time{}, false
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil && isPlainNumber(s) {
			return epochTime(f)
		}
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
			if parsed, err := time.Parse(layout, s); err == nil {
				return parsed.UTC(), true
			}
		}
	}
	return time.Time{}, false
}

func isPlainNumber(s string) bool {
	dot := false
	for i, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r == '.' && !dot && i > 0:
			dot = true
		default:
			return false
		}
	}
	return true
}

func epochTime(f float64) (time.Time, bool) {
	if math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return time.Time{}, false
	}
	var ms float64
	switch {
	case f < 1e11:
		ms = f * 1000
	case f < 1e14:
		ms = f
	case f < 1e17:
		ms = f / 1e3
	default:
		ms = f / 1e6
	}
	return time.Unix(0, int64(ms*1e6)).UTC(), true
}

// finiteNumber is a finite number from a number or a numeric string.
func finiteNumber(v any) (float64, bool) {
	var f float64
	switch t := v.(type) {
	case float64:
		f = t
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err != nil {
			return 0, false
		}
		f = parsed
	default:
		return 0, false
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

// firstNumber is the first of keys that is present and not null, read as a
// number (JS `a ?? b ?? c`, then finiteNumber).
func firstNumber(m map[string]any, keys ...string) (float64, bool) {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			return finiteNumber(v)
		}
	}
	return 0, false
}

// firstString is the first of keys holding a non-empty string or a number,
// "series" when none does.
func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			if v != "" {
				return v
			}
		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64)
		}
	}
	return "series"
}

// promLabel is name{k=v,...} from a Prometheus or Loki label set, "" for none.
func promLabel(v any) string {
	m, _ := v.(map[string]any)
	if len(m) == 0 {
		return ""
	}
	name, _ := m["__name__"].(string)
	keys := make([]string, 0, len(m))
	for k := range m {
		if k != "__name__" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+scalarText(m[k]))
	}
	switch {
	case len(parts) == 0:
		return name
	case name == "":
		return strings.Join(parts, ",")
	default:
		return name + "{" + strings.Join(parts, ",") + "}"
	}
}

// scalarText is a value as one short piece of text: strings as they are,
// numbers without exponent noise, anything else as compact JSON.
func scalarText(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case string:
		return t
	case float64:
		return number(t)
	case bool:
		return strconv.FormatBool(t)
	default:
		return compactJSON(t)
	}
}

func compactJSON(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprint(v)
	}
	return strings.TrimRight(buf.String(), "\n")
}

// number renders a value the way a person writes it: whole numbers without a
// decimal point, others to at most four decimals, tiny ones in short form.
func number(f float64) string {
	abs := math.Abs(f)
	switch {
	case f == math.Trunc(f) && abs < 1e15:
		return strconv.FormatFloat(f, 'f', 0, 64)
	case abs != 0 && abs < 0.001:
		return strconv.FormatFloat(f, 'g', 3, 64)
	default:
		s := strconv.FormatFloat(f, 'f', 4, 64)
		s = strings.TrimRight(s, "0")
		return strings.TrimSuffix(s, ".")
	}
}

// split is how many items to show from the head and the tail of a list of
// size total within a budget of n: all of it when it fits, else the first
// half and the last half (the head gets the odd one).
func split(total, n int) (int, int) {
	if total <= n {
		return total, 0
	}
	headN := (n + 1) / 2
	return headN, n - headN
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.UTC().Date()
	by, bm, bd := b.UTC().Date()
	return ay == by && am == bm && ad == bd
}

// clock is a line's time: HH:MM:SSZ, with the month and day in front when the
// answer spans more than one day.
func clock(t time.Time, multiDay bool) string {
	if multiDay {
		return t.UTC().Format("01-02 15:04:05") + "Z"
	}
	return t.UTC().Format("15:04:05") + "Z"
}

func timeRange(first, last time.Time) string {
	day := first.UTC().Format("2006-01-02 15:04:05") + "Z"
	switch {
	case first.Equal(last):
		return "at " + day
	case sameDay(first, last):
		return day + " to " + last.UTC().Format("15:04:05") + "Z"
	default:
		return day + " to " + last.UTC().Format("2006-01-02 15:04:05") + "Z"
	}
}

// plural is "1 event", "1,284 events".
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return grouped(n) + " " + many
}

func grouped(n int) string {
	s := strconv.Itoa(n)
	if n < 0 || len(s) <= 3 {
		return s
	}
	var out []byte
	pre := len(s) % 3
	if pre > 0 {
		out = append(out, s[:pre]...)
	}
	for i := pre; i < len(s); i += 3 {
		if len(out) > 0 {
			out = append(out, ',')
		}
		out = append(out, s[i:i+3]...)
	}
	return string(out)
}

func kb(n int) string {
	return grouped((n+1023)/1024) + " KB"
}

// cutBytes is s cut to at most n bytes, never inside a UTF-8 sequence.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
