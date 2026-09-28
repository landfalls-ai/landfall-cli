package tools

// widgetcheck.go: the canvas widget contracts, checked on this machine before
// a widget is queued or posted.
//
// WHY LOCALLY. share_with_room answers "shared" before anything reaches the
// room, so a widget the room refuses used to fail where the agent could not
// see it: the agent moved on believing its chart was on the canvas. The room
// checks every widget against its type's closed contract and refuses a
// mismatch (the monorepo widget catalog's validate.ts, called from
// edge.service.ts's contribute). This file is that contract, ported, so the
// agent gets the exact problem in the same tool result and can fix it and
// share again.
//
// THE SERVER STAYS AUTHORITATIVE. A widget that passes here can still be
// refused there (a contract that moved on, a limit this copy lags behind);
// that refusal reaches the agent through the refused-share line on its next
// room tool result. So this copy errs toward accepting: where it is stricter
// than the server, it is stricter only where the catalog itself says so (a
// time is ISO-8601, which the renderer needs to place it on an axis).
//
// Kept in the server's order and with the server's limits. When the server's
// contract changes, change this in the same two-repo rhythm as the widget
// type list in tools.go.

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/mcp"
)

// The server's caps (WIDGET_LIMITS in validate.ts).
const (
	maxWidgetSeries      = 8
	maxWidgetPoints      = 5000
	maxWidgetRows        = 2000
	maxWidgetLines       = 2000
	maxWidgetEvents      = 500
	maxWidgetGeoPoints   = 500
	maxWidgetAuditEvents = 500
	maxWidgetGraphNodes  = 120
	maxWidgetGraphEdges  = 400
)

var (
	widgetTones    = []string{"good", "warning", "serious", "critical", "neutral"}
	widgetOutcomes = []string{"success", "failure", "denied", "unknown"}
	widgetTrusts   = []string{"confirmed", "established", "inferred"}
)

// maxWidgetProblems is how many problems one error names; the rest are
// counted. An agent fixes the first few and tries again.
const maxWidgetProblems = 8

// ValidateWidget checks a widget payload against its type's contract: the
// type is one the room renders, the title is set, and the data has the shape
// the catalog describes (required keys, no unknown keys, numbers finite,
// times ISO-8601, series and point counts within the room's caps). It returns
// every problem found, in a stable order, or nil when the widget is fine.
func ValidateWidget(w *WidgetPayload) []string {
	c := &widgetChecker{}
	if w == nil {
		return []string{"widget must be an object: {widgetType, title, data}"}
	}
	known := false
	for _, t := range widgetTypes {
		if w.WidgetType == t {
			known = true
		}
	}
	if !known {
		if w.WidgetType == "" {
			c.errf("widget.widgetType is required (one of %s)", joinAny(widgetTypes))
		} else {
			c.errf("unknown widget type %s (known: %s)", quote(w.WidgetType), joinAny(widgetTypes))
		}
	}
	if strings.TrimSpace(w.Title) == "" {
		c.errf("widget.title is required: a few words saying what the widget shows")
	}
	if w.Data == nil {
		c.errf("widget.data must be an object holding the values to plot")
		return c.errs
	}
	if known {
		switch w.WidgetType {
		case "stat":
			c.stat(w.Data)
		case "chart":
			c.chart(w.Data)
		case "table":
			c.table(w.Data)
		case "logView":
			c.logView(w.Data)
		case "timeline":
			c.timeline(w.Data)
		case "geo":
			c.geo(w.Data)
		case "codeFinding":
			c.codeFinding(w.Data)
		case "events":
			c.events(w.Data)
		case "graph":
			c.graph(w.Data)
		}
	}
	return c.errs
}

// widgetProblem renders ValidateWidget's findings as the one error the agent
// sees, so it can fix the payload and try again. verb and past name what the
// tool does ("share"/"shared", "post"/"posted").
func widgetProblem(w *WidgetPayload, problems []string, verb, past string) error {
	what := "the widget"
	if w != nil && w.WidgetType != "" {
		what = "the " + quote(w.WidgetType) + " widget"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Nothing was %s: %s does not match the shape the room renders.\n", past, what)
	for i, p := range problems {
		if i == maxWidgetProblems {
			fmt.Fprintf(&sb, "- and %d more\n", len(problems)-maxWidgetProblems)
			break
		}
		fmt.Fprintf(&sb, "- %s\n", p)
	}
	fmt.Fprintf(&sb, "Fix these and %s it again. describe_widget_types lists every type's shape.", verb)
	return fmt.Errorf("%s", sb.String())
}

// checkWidget refuses a post_widget the room would refuse, before the
// narration wrapper posts it: that post is best-effort and its failure never
// reaches the agent, which would otherwise be told the widget was added.
func checkWidget(next mcp.Handler) mcp.Handler {
	return func(ctx context.Context, args map[string]any) (string, error) {
		data, _ := args["data"].(map[string]any)
		w := &WidgetPayload{WidgetType: str(args, "widgetType"), Title: str(args, "title"), Data: data}
		if problems := ValidateWidget(w); len(problems) > 0 {
			return "", widgetProblem(w, problems, "post", "posted")
		}
		return next(ctx, args)
	}
}

type widgetChecker struct{ errs []string }

func (c *widgetChecker) errf(format string, args ...any) {
	c.errs = append(c.errs, fmt.Sprintf(format, args...))
}

// closed refuses any key the contract does not name, in sorted order so the
// same payload always reports the same way.
func (c *widgetChecker) closed(obj map[string]any, allowed []string, where string) {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !contains(allowed, k) {
			c.errf("%s: unexpected key %s (allowed: %s)", where, quote(k), strings.Join(allowed, ", "))
		}
	}
}

func (c *widgetChecker) optString(d map[string]any, k, where string) {
	if v, ok := d[k]; ok && v != nil {
		if _, isStr := v.(string); !isStr {
			c.errf("%s.%s must be a string (got %s)", where, k, shown(v))
		}
	}
}

func (c *widgetChecker) optNumber(d map[string]any, k, where string) {
	if v, ok := d[k]; ok && v != nil && !finite(v) {
		c.errf("%s.%s must be a finite number (got %s)", where, k, shown(v))
	}
}

func (c *widgetChecker) reqString(d map[string]any, k, where string) {
	if _, ok := d[k].(string); !ok {
		c.errf("%s.%s must be a string (got %s)", where, k, shown(d[k]))
	}
}

func (c *widgetChecker) reqNonEmpty(d map[string]any, k, where string) {
	if s, ok := d[k].(string); !ok || s == "" {
		c.errf("%s.%s must be a non-empty string (got %s)", where, k, shown(d[k]))
	}
}

func (c *widgetChecker) optEnum(d map[string]any, k, where string, allowed []string) {
	v, ok := d[k]
	if !ok || v == nil {
		return
	}
	if s, isStr := v.(string); !isStr || !contains(allowed, s) {
		c.errf("%s.%s must be one of %s (got %s)", where, k, strings.Join(allowed, ", "), shown(v))
	}
}

func (c *widgetChecker) tone(d map[string]any, k, where string) {
	c.optEnum(d, k, where, widgetTones)
}

// isoTime requires d[k] to be an ISO-8601 time string. required=false lets it
// be absent.
func (c *widgetChecker) isoTime(d map[string]any, k, where string, required bool) {
	v, ok := d[k]
	if !ok || v == nil {
		if required {
			c.errf("%s.%s is required: an ISO-8601 time such as 2026-09-28T14:05:00Z", where, k)
		}
		return
	}
	s, isStr := v.(string)
	if !isStr || !isISOTime(s) {
		c.errf("%s.%s must be an ISO-8601 time such as 2026-09-28T14:05:00Z (got %s)", where, k, shown(v))
	}
}

func (c *widgetChecker) object(v any, where string) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		c.errf("%s must be an object (got %s)", where, shown(v))
	}
	return m, ok
}

func (c *widgetChecker) array(d map[string]any, k, where string) ([]any, bool) {
	a, ok := d[k].([]any)
	if !ok {
		c.errf("%s.%s must be an array (got %s)", where, k, shown(d[k]))
	}
	return a, ok
}

func (c *widgetChecker) entity(v any, where string) {
	m, ok := c.object(v, where)
	if !ok {
		return
	}
	c.closed(m, []string{"kind", "id", "label"}, where)
	if k, isStr := m["kind"].(string); !isStr || !lowerToken(k) {
		c.errf("%s.kind must be a lower-case token of at most 40 characters (got %s)", where, shown(m["kind"]))
	}
	if id, isStr := m["id"].(string); !isStr || id == "" || len(id) > 512 {
		c.errf("%s.id must be 1 to 512 characters (got %s)", where, shown(m["id"]))
	}
	if l, present := m["label"]; present && l != nil {
		if s, isStr := l.(string); !isStr || len(s) > 200 {
			c.errf("%s.label must be a string of at most 200 characters", where)
		}
	}
}

// --- the nine types ---------------------------------------------------------

func (c *widgetChecker) stat(d map[string]any) {
	c.closed(d, []string{"value", "unit", "decimals", "delta", "deltaLabel", "trend", "tone", "baseline", "baselineLabel", "spark"}, "stat")
	if !finite(d["value"]) {
		c.errf("stat.value must be a finite number (got %s)", shown(d["value"]))
	}
	c.optString(d, "unit", "stat")
	c.optNumber(d, "decimals", "stat")
	c.optNumber(d, "delta", "stat")
	c.optString(d, "deltaLabel", "stat")
	c.optEnum(d, "trend", "stat", []string{"up", "down", "flat"})
	c.tone(d, "tone", "stat")
	c.optNumber(d, "baseline", "stat")
	c.optString(d, "baselineLabel", "stat")
	if v, ok := d["spark"]; ok && v != nil {
		a, isArr := v.([]any)
		switch {
		case !isArr:
			c.errf("stat.spark must be an array of finite numbers (got %s)", shown(v))
		case len(a) > maxWidgetPoints:
			c.errf("stat.spark holds %d values; the cap is %d", len(a), maxWidgetPoints)
		default:
			for i, x := range a {
				if !finite(x) {
					c.errf("stat.spark[%d] must be a finite number (got %s)", i, shown(x))
				}
			}
		}
	}
}

func (c *widgetChecker) chart(d map[string]any) {
	c.closed(d, []string{"series", "yAxis", "thresholds", "markers", "window", "folded"}, "chart")
	if series, ok := c.array(d, "series", "chart"); ok {
		if len(series) > maxWidgetSeries {
			c.errf("chart.series holds %d series; the cap is %d (fold the rest into fewer series)", len(series), maxWidgetSeries)
		}
		for i, s := range series {
			w := fmt.Sprintf("chart.series[%d]", i)
			sm, ok := c.object(s, w)
			if !ok {
				continue
			}
			c.closed(sm, []string{"label", "unit", "decimals", "points"}, w)
			c.reqString(sm, "label", w)
			c.optString(sm, "unit", w)
			c.optNumber(sm, "decimals", w)
			points, ok := c.array(sm, "points", w)
			if !ok {
				continue
			}
			if len(points) > maxWidgetPoints {
				c.errf("%s.points holds %d points; the cap is %d", w, len(points), maxWidgetPoints)
				continue
			}
			for j, p := range points {
				pw := fmt.Sprintf("%s.points[%d]", w, j)
				pm, ok := c.object(p, pw)
				if !ok {
					continue
				}
				c.closed(pm, []string{"t", "v"}, pw)
				c.isoTime(pm, "t", pw, true)
				if !finite(pm["v"]) {
					c.errf("%s.v must be a finite number (got %s)", pw, shown(pm["v"]))
				}
			}
		}
	}
	if v, ok := d["yAxis"]; ok && v != nil {
		if y, ok := c.object(v, "chart.yAxis"); ok {
			c.closed(y, []string{"min", "max", "unit", "label"}, "chart.yAxis")
			c.optNumber(y, "min", "chart.yAxis")
			c.optNumber(y, "max", "chart.yAxis")
			c.optString(y, "unit", "chart.yAxis")
			c.optString(y, "label", "chart.yAxis")
		}
	}
	if v, ok := d["thresholds"]; ok && v != nil {
		if th, ok := c.array(d, "thresholds", "chart"); ok {
			for i, t := range th {
				w := fmt.Sprintf("chart.thresholds[%d]", i)
				tm, ok := c.object(t, w)
				if !ok {
					continue
				}
				c.closed(tm, []string{"value", "label", "tone"}, w)
				if !finite(tm["value"]) {
					c.errf("%s.value must be a finite number (got %s)", w, shown(tm["value"]))
				}
				c.optString(tm, "label", w)
				c.tone(tm, "tone", w)
			}
		}
	}
	if v, ok := d["markers"]; ok && v != nil {
		if ms, ok := c.array(d, "markers", "chart"); ok {
			for i, m := range ms {
				w := fmt.Sprintf("chart.markers[%d]", i)
				mm, ok := c.object(m, w)
				if !ok {
					continue
				}
				c.closed(mm, []string{"t", "label", "kind"}, w)
				c.isoTime(mm, "t", w, true)
				c.reqString(mm, "label", w)
				c.optString(mm, "kind", w)
			}
		}
	}
	c.window(d, "chart")
	c.optNumber(d, "folded", "chart")
}

func (c *widgetChecker) window(d map[string]any, where string) {
	v, ok := d["window"]
	if !ok || v == nil {
		return
	}
	w := where + ".window"
	wm, ok := c.object(v, w)
	if !ok {
		return
	}
	c.closed(wm, []string{"from", "to"}, w)
	c.reqString(wm, "from", w)
	c.reqString(wm, "to", w)
}

func (c *widgetChecker) table(d map[string]any) {
	c.closed(d, []string{"columns", "rows", "highlightKey"}, "table")
	if cols, ok := c.array(d, "columns", "table"); ok {
		for i, col := range cols {
			w := fmt.Sprintf("table.columns[%d]", i)
			cm, ok := c.object(col, w)
			if !ok {
				continue
			}
			c.closed(cm, []string{"key", "label", "type"}, w)
			c.reqString(cm, "key", w)
			c.reqString(cm, "label", w)
			if s, _ := cm["type"].(string); !contains([]string{"string", "number", "time", "status", "share"}, s) {
				c.errf("%s.type must be one of string, number, time, status, share (got %s)", w, shown(cm["type"]))
			}
		}
	}
	if rows, ok := c.array(d, "rows", "table"); ok {
		if len(rows) > maxWidgetRows {
			c.errf("table.rows holds %d rows; the cap is %d", len(rows), maxWidgetRows)
		} else {
			for i, r := range rows {
				w := fmt.Sprintf("table.rows[%d]", i)
				rm, ok := c.object(r, w)
				if !ok {
					continue
				}
				keys := make([]string, 0, len(rm))
				for k := range rm {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					c.cell(rm[k], w+"."+k)
				}
			}
		}
	}
	c.optString(d, "highlightKey", "table")
}

func (c *widgetChecker) cell(v any, where string) {
	if _, isStr := v.(string); isStr || finite(v) {
		return
	}
	m, ok := v.(map[string]any)
	if !ok {
		c.errf("%s must be a string, a number, or {v, tone?} (got %s)", where, shown(v))
		return
	}
	c.closed(m, []string{"v", "tone"}, where)
	if _, isStr := m["v"].(string); !isStr && !finite(m["v"]) {
		c.errf("%s.v must be a string or a number (got %s)", where, shown(m["v"]))
	}
	c.tone(m, "tone", where)
}

func (c *widgetChecker) logView(d map[string]any) {
	c.closed(d, []string{"lines", "truncated"}, "logView")
	if lines, ok := c.array(d, "lines", "logView"); ok {
		if len(lines) > maxWidgetLines {
			c.errf("logView.lines holds %d lines; the cap is %d", len(lines), maxWidgetLines)
		} else {
			for i, l := range lines {
				w := fmt.Sprintf("logView.lines[%d]", i)
				lm, ok := c.object(l, w)
				if !ok {
					continue
				}
				c.closed(lm, []string{"t", "level", "message", "fields"}, w)
				c.reqString(lm, "message", w)
				c.isoTime(lm, "t", w, false)
				c.optString(lm, "level", w)
				if f, present := lm["fields"]; present && f != nil {
					c.object(f, w+".fields")
				}
			}
		}
	}
	c.optBool(d, "truncated", "logView")
}

func (c *widgetChecker) optBool(d map[string]any, k, where string) {
	if v, ok := d[k]; ok && v != nil {
		if _, isBool := v.(bool); !isBool {
			c.errf("%s.%s must be true or false (got %s)", where, k, shown(v))
		}
	}
}

func (c *widgetChecker) timeline(d map[string]any) {
	c.closed(d, []string{"events"}, "timeline")
	evs, ok := c.array(d, "events", "timeline")
	if !ok {
		return
	}
	if len(evs) > maxWidgetEvents {
		c.errf("timeline.events holds %d events; the cap is %d", len(evs), maxWidgetEvents)
		return
	}
	for i, ev := range evs {
		w := fmt.Sprintf("timeline.events[%d]", i)
		em, ok := c.object(ev, w)
		if !ok {
			continue
		}
		c.closed(em, []string{"t", "label", "kind", "detail", "correlated", "tone"}, w)
		c.isoTime(em, "t", w, true)
		c.reqString(em, "label", w)
		c.optString(em, "kind", w)
		c.optString(em, "detail", w)
		if v, present := em["correlated"]; present && v != nil {
			if _, isBool := v.(bool); !isBool && !finite(v) {
				c.errf("%s.correlated must be true, false or a number (got %s)", w, shown(v))
			}
		}
		c.tone(em, "tone", w)
	}
}

func (c *widgetChecker) geo(d map[string]any) {
	c.closed(d, []string{"points", "unit", "legend"}, "geo")
	if pts, ok := c.array(d, "points", "geo"); ok {
		if len(pts) > maxWidgetGeoPoints {
			c.errf("geo.points holds %d places; the cap is %d", len(pts), maxWidgetGeoPoints)
		} else {
			for i, p := range pts {
				w := fmt.Sprintf("geo.points[%d]", i)
				pm, ok := c.object(p, w)
				if !ok {
					continue
				}
				c.closed(pm, []string{"id", "place", "label", "lat", "lon", "value", "unit", "tone", "pulse", "detail", "peakTone"}, w)
				c.reqNonEmpty(pm, "place", w)
				c.optString(pm, "id", w)
				c.optString(pm, "label", w)
				c.inRange(pm, "lat", w, -90, 90)
				c.inRange(pm, "lon", w, -180, 180)
				c.optNumber(pm, "value", w)
				c.optString(pm, "unit", w)
				c.tone(pm, "tone", w)
				c.tone(pm, "peakTone", w)
				c.optBool(pm, "pulse", w)
				c.optString(pm, "detail", w)
			}
		}
	}
	c.optString(d, "unit", "geo")
	c.optString(d, "legend", "geo")
}

func (c *widgetChecker) inRange(d map[string]any, k, where string, lo, hi float64) {
	v, ok := d[k]
	if !ok || v == nil {
		return
	}
	n, isNum := jsNumber(v)
	if !isNum || math.IsNaN(n) || math.IsInf(n, 0) || n < lo || n > hi {
		c.errf("%s.%s must be a number from %g to %g (got %s)", where, k, lo, hi, shown(v))
	}
}

func (c *widgetChecker) codeFinding(d map[string]any) {
	c.closed(d, []string{"codeRef", "snippet"}, "codeFinding")
	if ref, ok := c.object(d["codeRef"], "codeFinding.codeRef"); ok {
		w := "codeFinding.codeRef"
		c.closed(ref, []string{"provider", "owner", "repo", "ref", "path", "lineStart", "lineEnd", "pullRequestNumber", "permalink"}, w)
		for _, k := range []string{"owner", "repo", "ref", "permalink"} {
			c.reqNonEmpty(ref, k, w)
		}
		if p, isStr := ref["permalink"].(string); isStr && p != "" && !strings.HasPrefix(p, "https://") {
			c.errf("%s.permalink must be an https URL (got %s)", w, shown(p))
		}
		c.optString(ref, "path", w)
		c.optNumber(ref, "lineStart", w)
		c.optNumber(ref, "lineEnd", w)
		c.optNumber(ref, "pullRequestNumber", w)
	}
	if sn, ok := c.object(d["snippet"], "codeFinding.snippet"); ok {
		w := "codeFinding.snippet"
		c.closed(sn, []string{"lines", "startLine"}, w)
		lines, isArr := sn["lines"].([]any)
		allStrings := isArr
		for _, l := range lines {
			if _, isStr := l.(string); !isStr {
				allStrings = false
			}
		}
		if !allStrings {
			c.errf("%s.lines must be an array of strings", w)
		}
		if !finite(sn["startLine"]) {
			c.errf("%s.startLine must be a number (got %s)", w, shown(sn["startLine"]))
		}
	}
}

func (c *widgetChecker) events(d map[string]any) {
	c.closed(d, []string{"events", "truncated", "window"}, "events")
	if evs, ok := c.array(d, "events", "events"); ok {
		if len(evs) > maxWidgetAuditEvents {
			c.errf("events.events holds %d events; the cap is %d", len(evs), maxWidgetAuditEvents)
		} else {
			for i, ev := range evs {
				w := fmt.Sprintf("events.events[%d]", i)
				em, ok := c.object(ev, w)
				if !ok {
					continue
				}
				c.closed(em, []string{"t", "actor", "action", "target", "outcome", "sourceIp", "userAgent", "errorCode", "detail"}, w)
				c.isoTime(em, "t", w, true)
				c.reqNonEmpty(em, "action", w)
				if am, ok := c.object(em["actor"], w+".actor"); ok {
					aw := w + ".actor"
					c.closed(am, []string{"kind", "id", "label"}, aw)
					if k, isStr := am["kind"].(string); !isStr || !lowerToken(k) {
						c.errf("%s.kind must be a lower-case token of at most 40 characters (got %s)", aw, shown(am["kind"]))
					}
					if id, isStr := am["id"].(string); !isStr || id == "" || len(id) > 512 {
						c.errf("%s.id must be 1 to 512 characters (got %s)", aw, shown(am["id"]))
					}
					c.optString(am, "label", aw)
				}
				if tg, present := em["target"]; present && tg != nil {
					c.entity(tg, w+".target")
				}
				c.optEnum(em, "outcome", w, widgetOutcomes)
				for _, k := range []string{"sourceIp", "userAgent", "errorCode", "detail"} {
					c.optString(em, k, w)
				}
			}
		}
	}
	c.optBool(d, "truncated", "events")
	c.window(d, "events")
}

func (c *widgetChecker) graph(d map[string]any) {
	c.closed(d, []string{"nodes", "edges", "focus", "omitted", "legend"}, "graph")
	ids := map[string]bool{}
	if nodes, ok := c.array(d, "nodes", "graph"); ok {
		if len(nodes) > maxWidgetGraphNodes {
			c.errf("graph.nodes holds %d nodes; the cap is %d", len(nodes), maxWidgetGraphNodes)
		} else {
			for i, n := range nodes {
				w := fmt.Sprintf("graph.nodes[%d]", i)
				nm, ok := c.object(n, w)
				if !ok {
					continue
				}
				c.closed(nm, []string{"id", "label", "kind", "tone", "entity", "trust", "detail"}, w)
				id, isStr := nm["id"].(string)
				switch {
				case !isStr || id == "":
					c.errf("%s.id must be a non-empty string (got %s)", w, shown(nm["id"]))
				case ids[id]:
					c.errf("%s.id %s is not unique", w, quote(id))
				default:
					ids[id] = true
				}
				c.reqNonEmpty(nm, "label", w)
				c.optString(nm, "kind", w)
				c.optString(nm, "detail", w)
				c.tone(nm, "tone", w)
				c.optEnum(nm, "trust", w, widgetTrusts)
				if e, present := nm["entity"]; present && e != nil {
					c.entity(e, w+".entity")
				}
			}
		}
	}
	if edges, ok := c.array(d, "edges", "graph"); ok {
		if len(edges) > maxWidgetGraphEdges {
			c.errf("graph.edges holds %d edges; the cap is %d", len(edges), maxWidgetGraphEdges)
		} else {
			for i, e := range edges {
				w := fmt.Sprintf("graph.edges[%d]", i)
				em, ok := c.object(e, w)
				if !ok {
					continue
				}
				c.closed(em, []string{"from", "to", "label", "kind", "tone", "trust", "direction"}, w)
				for _, k := range []string{"from", "to"} {
					end, isStr := em[k].(string)
					switch {
					case !isStr || end == "":
						c.errf("%s.%s must be a non-empty string (got %s)", w, k, shown(em[k]))
					case !ids[end]:
						c.errf("%s.%s names no node in graph.nodes (got %s)", w, k, quote(end))
					}
				}
				c.optString(em, "label", w)
				c.optString(em, "kind", w)
				c.tone(em, "tone", w)
				c.optEnum(em, "trust", w, widgetTrusts)
				c.optEnum(em, "direction", w, []string{"forward", "both", "none"})
			}
		}
	}
	if v, ok := d["focus"]; ok && v != nil {
		if f, isStr := v.(string); !isStr {
			c.errf("graph.focus must be a string (got %s)", shown(v))
		} else if !ids[f] {
			c.errf("graph.focus names no node in graph.nodes (got %s)", quote(f))
		}
	}
	if v, ok := d["omitted"]; ok && v != nil {
		if n, isNum := jsNumber(v); !isNum || !finite(v) || n < 0 {
			c.errf("graph.omitted must be a non-negative number (got %s)", shown(v))
		}
	}
	c.optString(d, "legend", "graph")
}

// --- helpers ----------------------------------------------------------------

// finite is JS's `typeof v === 'number' && Number.isFinite(v)`.
func finite(v any) bool {
	n, ok := jsNumber(v)
	return ok && !math.IsNaN(n) && !math.IsInf(n, 0)
}

// isoLayouts are the ISO-8601 forms a browser's Date parses and the room's
// renderers place on an axis. Fractional seconds are accepted after the
// seconds field by every layout that has one.
var isoLayouts = []string{
	time.RFC3339,
	"2006-01-02T15:04:05Z0700",
	"2006-01-02T15:04:05",
	"2006-01-02T15:04Z07:00",
	"2006-01-02T15:04",
	"2006-01-02",
}

// isISOTime reports whether s is an ISO-8601 date or date-time. A space in
// place of the T is accepted, as a browser's Date accepts it.
func isISOTime(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) >= 11 && s[10] == ' ' {
		s = s[:10] + "T" + s[11:]
	}
	for _, layout := range isoLayouts {
		if _, err := time.Parse(layout, s); err == nil {
			return true
		}
	}
	return false
}

// lowerToken is the entity-kind rule: a lower-case token of at most 40
// characters, starting with a letter.
func lowerToken(s string) bool {
	if s == "" || len(s) > 40 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// shown renders an offending value for an error: short, and quoted when it is
// text, so "12" (a string) and 12 (a number) read differently.
func shown(v any) string {
	switch x := v.(type) {
	case nil:
		return "nothing"
	case string:
		return quote(snippet(x, 40))
	case bool:
		return fmt.Sprint(x)
	case map[string]any:
		return "an object"
	case []any:
		return fmt.Sprintf("an array of %d", len(x))
	}
	if n, ok := jsNumber(v); ok {
		return fmt.Sprint(n)
	}
	return fmt.Sprintf("%T", v)
}
