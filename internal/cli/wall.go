package cli

// wall.go — `landfall wall --room <k>` (contract §5, spec FR-06): the room's
// canvas in the shared arrangement, each widget flattened to what a pane
// draws.
//
// Which widgets: the war room's canvas is a fold of the timeline's
// agent.widget.* events (`@landfall/widget-catalog` projectWidgets), not the
// older dashboard version record GET …/dashboard serves, so this folds the
// same events the same way the hosted connector's canvas read does
// (core-api mcp-gateway incident-reader.ts `canvas`). Failed widgets are left
// out, as there.
//
// Which order: the latest canvas.layout.saved for scope "shared"
// (warroom-ui sharedLayout.ts latestSharedLayout); widgets it does not name
// follow in the order they were built.
//
// Which data, and as whom: a widget whose data is on the timeline (data-only
// or platform-built) is drawn from that. A query-backed one (it has a
// moduleId) carries no data on the timeline; it is re-executed per viewer
// through the credential wall by POST …/widgets/data, which resolves as the
// CALLER (`req.user.sub`). The room's session is an edge token whose subject
// is whoever minted the share link, which is not necessarily this person, so
// the data is read with the person's own `landfall login` token. Without one,
// or signed in to another organization, those widgets are listed under
// `unavailable` with the reason, never read as somebody else.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/landfalls-ai/landfall-cli/internal/hooks"
	"github.com/spf13/cobra"
)

const (
	// wallWidgetsMax is the war-room canvas's own live batch cap.
	wallWidgetsMax  = 40
	wallPointsMax   = 240
	wallRowsMax     = 50
	wallLinesMax    = 50
	wallItemsMax    = 60
	wallNodesMax    = 60
	wallCellTextMax = 120
)

func newWallCommand(ui *UI) *cobra.Command {
	return newReadCommand(ui, "wall", func(cmd *cobra.Command, ws hooks.Workspace, room string) map[string]any {
		return RunWall(cmdContext(cmd), room, defaultReadDeps(ws))
	})
}

// projectedWidget is one widget folded from its agent.widget.* events.
type projectedWidget struct {
	id, typ, title, status, moduleID, errText string
	data                                      map[string]any
	// groupPlaces is, per query of a grouped build (validated
	// build.scope.query), the value of the dimension the widget is grouped by,
	// in query order: what each series is about when its own label is not.
	groupPlaces []string
}

// projectWall is projectWidgets (widget-catalog project.ts), in build order.
func projectWall(events []timelineEvent) []*projectedWidget {
	byID := map[string]*projectedWidget{}
	var order []string
	for _, e := range events {
		if !strings.HasPrefix(e.Type, "agent.widget.") {
			continue
		}
		p := e.Payload
		id := jStr(p, "widgetId")
		if id == "" {
			continue
		}
		w, ok := byID[id]
		if !ok {
			w = &projectedWidget{id: id, typ: "chart", title: id, status: "pending"}
			byID[id] = w
			order = append(order, id)
		}
		switch e.Type {
		case "agent.widget.requested":
			if t := jStr(p, "title"); t != "" {
				w.title = t
			}
			if t := jStr(p, "widgetType"); t != "" {
				w.typ = t
			}
		case "agent.widget.codegen":
			if t := jStr(p, "type"); t != "" {
				w.typ = t
			}
		case "agent.widget.validated":
			w.moduleID = jStr(p, "moduleId")
			w.groupPlaces = groupPlaces(jObj(jObj(jObj(p, "build"), "scope"), "query"))
			if t := jStr(p, "type"); t != "" {
				w.typ = t
			}
			if t := jStr(p, "title"); t != "" {
				w.title = t
			}
		case "agent.widget.executed":
			if t := jStr(p, "type"); t != "" {
				w.typ = t
			}
			w.status = "rendered"
			if jStr(p, "status") == "no-data" {
				w.status = "no-data"
			}
			w.data = jObj(p, "data")
		case "agent.widget.failed":
			w.status = "failed"
			w.errText = jStr(p, "error")
		}
	}
	out := make([]*projectedWidget, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out
}

// sharedArrangement is the latest canvas.layout.saved for the shared wall:
// its order and who shared it.
func sharedArrangement(events []timelineEvent) (order []string, sharedBy string, found bool) {
	best := int64(-1)
	for _, e := range events {
		if e.Type != "canvas.layout.saved" || e.Seq == nil || *e.Seq <= best {
			continue
		}
		if jStr(e.Payload, "scope") != "shared" {
			continue
		}
		layout := jObj(e.Payload, "layout")
		if v, ok := jNum(layout, "v"); !ok || v != 1 {
			continue
		}
		var ids []string
		for _, id := range jList(layout, "order") {
			if s, ok := id.(string); ok && s != "" {
				ids = append(ids, s)
			}
		}
		best, order, sharedBy, found = *e.Seq, ids, jStr(e.Payload, "displayName"), true
	}
	return order, sharedBy, found
}

// RunWall builds the wall answer.
func RunWall(ctx context.Context, roomSel string, d ReadDeps) map[string]any {
	room, why := pickRoom(d, roomSel)
	if why != "" {
		return failure(why)
	}
	raw, err := d.RoomRead(ctx, room.RoomKey, "/events")
	if err != nil {
		return failure(roomRefusal(err, "the room's canvas"))
	}
	var events []timelineEvent
	if err := json.Unmarshal(raw, &events); err != nil {
		return failure("Landfall sent a timeline this CLI could not read.")
	}

	var widgets []*projectedWidget
	for _, w := range projectWall(events) {
		if w.status != "failed" {
			widgets = append(widgets, w)
		}
	}
	order, sharedBy, _ := sharedArrangement(events)
	widgets = arrange(widgets, order)
	total := len(widgets)
	if len(widgets) > wallWidgetsMax {
		widgets = widgets[:wallWidgetsMax]
	}

	// Query-backed widgets are resolved as the person, in one batch.
	var toResolve []string
	for _, w := range widgets {
		if w.moduleID != "" {
			toResolve = append(toResolve, w.id)
		}
	}
	resolved := map[string]map[string]any{}
	unreadable := map[string]string{}
	windowMs := int64(0)
	if len(toResolve) > 0 {
		reason := ""
		org, err := d.Org(ctx)
		switch {
		case err != nil || org == nil:
			reason = "Sign in to read this widget as yourself: run landfall login."
		case org.Slug != room.Slug:
			reason = "You are signed in to another organization, so this widget cannot be read as you."
		default:
			body, err := org.WidgetsData(ctx, room.IncidentID, toResolve)
			if err != nil {
				reason = personRefusal(err, "this widget's data")
			} else {
				var batch struct {
					Window  map[string]any            `json:"window"`
					Widgets map[string]map[string]any `json:"widgets"`
				}
				if json.Unmarshal(body, &batch) != nil {
					reason = "Landfall sent widget data this CLI could not read."
				} else {
					if from, to := parseTime(jStr(batch.Window, "from")), parseTime(jStr(batch.Window, "to")); !from.IsZero() && to.After(from) {
						windowMs = to.Sub(from).Milliseconds()
					}
					for id, r := range batch.Widgets {
						switch jStr(r, "status") {
						case "rendered", "no-data":
							resolved[id] = jObj(r, "data")
							if resolved[id] == nil {
								resolved[id] = map[string]any{}
							}
						default:
							msg := jStr(jObj(r, "error"), "message")
							if msg == "" {
								msg = "Landfall could not read this widget for you."
							}
							unreadable[id] = oneLineText(msg, 160)
						}
					}
				}
			}
		}
		if reason != "" {
			for _, id := range toResolve {
				unreadable[id] = reason
			}
		}
	}

	out := make([]map[string]any, 0, len(widgets))
	unavailable := make([]map[string]any, 0)
	for _, w := range widgets {
		data := w.data
		if w.moduleID != "" {
			if why, bad := unreadable[w.id]; bad {
				unavailable = append(unavailable, map[string]any{"id": w.id, "type": w.typ, "title": w.title, "reason": why})
				continue
			}
			data = resolved[w.id]
		}
		flat := flattenWidget(w.id, w.typ, w.title, data)
		if w.typ == "geo" {
			nameGroupedPlaces(flat, w.groupPlaces)
		}
		out = append(out, flat)
	}
	ans := map[string]any{"ok": true, "widgets": out, "unavailable": unavailable}
	if sharedBy != "" {
		ans["sharedBy"] = sharedBy
	}
	if windowMs > 0 {
		ans["windowMs"] = windowMs
	}
	if total > len(widgets) {
		ans["totalWidgets"] = total
	}
	return ans
}

// firstStr is the first non-blank string among keys.
func firstStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := strings.TrimSpace(jStr(m, k)); s != "" {
			return s
		}
	}
	return ""
}

// groupPlaces reads a grouped build's queries: for each CloudWatch metric
// query, the value of the dimension named by groupBy ("Region" ->
// "eu-west-1"). Nil when the build is not grouped or a query names no such
// dimension, so a guess is never made.
func groupPlaces(query map[string]any) []string {
	by := strings.TrimSpace(jStr(query, "groupBy"))
	if by == "" {
		return nil
	}
	var out []string
	for _, q := range jList(jObj(query, "params"), "MetricDataQueries") {
		qm, _ := q.(map[string]any)
		if rd, ok := qm["ReturnData"].(bool); ok && !rd {
			continue // an input to an expression draws no series
		}
		place := ""
		for _, d := range jList(jObj(jObj(qm, "MetricStat"), "Metric"), "Dimensions") {
			dm, _ := d.(map[string]any)
			if strings.EqualFold(jStr(dm, "Name"), by) {
				place = strings.TrimSpace(jStr(dm, "Value"))
				break
			}
		}
		if place == "" {
			return nil
		}
		out = append(out, place)
	}
	return out
}

// nameGroupedPlaces gives a geo widget's points their place names back when
// the server could not. The server names each point after its series' label,
// and a query labelled with the dimension's NAME ("Label": "Region" on every
// query, as a local model wrote it in a live run) comes back as five points
// all called "Region", which a pane draws as "Region 0.28" five times and a
// map cannot place. When the places do not tell the points apart and the
// build's queries do, one point per query in query order, each point takes
// its query's dimension value; a label that repeated the old name is
// renamed with it.
func nameGroupedPlaces(w map[string]any, places []string) {
	pts, _ := w["points"].([]map[string]any)
	if len(pts) == 0 || len(pts) != len(places) {
		return
	}
	seen := map[string]bool{}
	distinct := true
	for _, p := range pts {
		name, _ := p["place"].(string)
		if name == "" || seen[name] {
			distinct = false
			break
		}
		seen[name] = true
	}
	if distinct {
		return
	}
	want := map[string]bool{}
	for _, pl := range places {
		if want[pl] {
			return // the queries do not tell the points apart either
		}
		want[pl] = true
	}
	for i, p := range pts {
		old, _ := p["place"].(string)
		p["place"] = places[i]
		if label, _ := p["label"].(string); old != "" && strings.HasPrefix(label, old) {
			p["label"] = places[i] + strings.TrimPrefix(label, old)
		}
	}
}

// arrange puts the widgets the shared arrangement names first, in its order.
func arrange(widgets []*projectedWidget, order []string) []*projectedWidget {
	if len(order) == 0 {
		return widgets
	}
	byID := map[string]*projectedWidget{}
	for _, w := range widgets {
		byID[w.id] = w
	}
	placed := map[string]bool{}
	out := make([]*projectedWidget, 0, len(widgets))
	for _, id := range order {
		if w, ok := byID[id]; ok && !placed[id] {
			out = append(out, w)
			placed[id] = true
		}
	}
	for _, w := range widgets {
		if !placed[w.id] {
			out = append(out, w)
		}
	}
	return out
}

// flattenWidget shapes one widget's data for a pane (widget-catalog types.ts).
// A type this CLI does not know passes through with type and title only.
func flattenWidget(id, typ, title string, data map[string]any) map[string]any {
	w := map[string]any{"id": id, "type": typ, "title": oneLineText(title, 120)}
	if data == nil {
		w["empty"] = true
		return w
	}
	switch typ {
	case "chart":
		var series []map[string]any
		for _, s := range jList(data, "series") {
			sm, _ := s.(map[string]any)
			var pts [][2]float64
			for _, p := range jList(sm, "points") {
				pm, _ := p.(map[string]any)
				t := parseTime(jStr(pm, "t"))
				v, ok := jNum(pm, "v")
				if t.IsZero() || !ok {
					continue
				}
				pts = append(pts, [2]float64{float64(t.UnixMilli()), v})
			}
			pts = thinPairs(pts, wallPointsMax)
			one := map[string]any{"label": jStr(sm, "label"), "points": pts}
			if u := jStr(sm, "unit"); u != "" {
				one["unit"] = u
			}
			series = append(series, one)
		}
		w["series"] = series
		var markers []map[string]any
		for _, m := range jList(data, "markers") {
			mm, _ := m.(map[string]any)
			if t := parseTime(jStr(mm, "t")); !t.IsZero() {
				markers = append(markers, map[string]any{"atMs": t.UnixMilli(), "label": jStr(mm, "label")})
			}
		}
		if len(markers) > 0 {
			w["markers"] = markers
		}
		if th := jList(data, "thresholds"); len(th) > 0 {
			tm, _ := th[0].(map[string]any)
			if v, ok := jNum(tm, "value"); ok {
				w["threshold"] = v
				if tone := jStr(tm, "tone"); tone != "" {
					w["tone"] = tone
				}
			}
		}
		if u := jStr(jObj(data, "yAxis"), "unit"); u != "" {
			w["unit"] = u
		}
	case "stat":
		v, ok := jNum(data, "value")
		if ok {
			decimals := 0
			if dv, has := jNum(data, "decimals"); has && dv >= 0 && dv <= 6 {
				decimals = int(dv)
			}
			w["value"] = strconv.FormatFloat(v, 'f', decimals, 64)
		}
		for _, key := range []string{"unit", "tone", "trend", "deltaLabel", "baselineLabel"} {
			if s := jStr(data, key); s != "" {
				w[key] = s
			}
		}
		if delta, ok := jNum(data, "delta"); ok {
			w["delta"] = delta
		}
		var spark []float64
		for _, s := range jList(data, "spark") {
			if f, ok := s.(float64); ok {
				spark = append(spark, f)
			}
		}
		if len(spark) > 0 {
			w["spark"] = spark
		}
	case "logView":
		var lines []map[string]any
		for _, l := range jList(data, "lines") {
			if len(lines) == wallLinesMax {
				break
			}
			lm, _ := l.(map[string]any)
			line := map[string]any{"level": jStr(lm, "level"), "text": oneLineText(jStr(lm, "message"), 240)}
			if t := jStr(lm, "t"); t != "" {
				line["at"] = t
			}
			lines = append(lines, line)
		}
		w["lines"] = lines
	case "graph":
		var nodes, edges []map[string]any
		for _, n := range jList(data, "nodes") {
			if len(nodes) == wallNodesMax {
				break
			}
			nm, _ := n.(map[string]any)
			node := map[string]any{"id": jStr(nm, "id"), "label": oneLineText(jStr(nm, "label"), 60)}
			for _, key := range []string{"tone", "kind", "trust"} {
				if s := jStr(nm, key); s != "" {
					node[key] = s
				}
			}
			// A node that names its resource takes its badge from the entity
			// (widget-catalog GraphNode.kind), never from a kind beside it.
			if ek := jStr(jObj(nm, "entity"), "kind"); ek != "" {
				node["kind"] = ek
			}
			nodes = append(nodes, node)
		}
		for _, e := range jList(data, "edges") {
			em, _ := e.(map[string]any)
			edge := map[string]any{"from": jStr(em, "from"), "to": jStr(em, "to")}
			for _, key := range []string{"trust", "label", "tone", "direction"} {
				if s := jStr(em, key); s != "" {
					edge[key] = s
				}
			}
			edges = append(edges, edge)
		}
		w["nodes"], w["edges"] = nodes, edges
		if f := jStr(data, "focus"); f != "" {
			w["focus"] = f
		}
	case "table":
		var keys, labels []string
		for _, c := range jList(data, "columns") {
			cm, _ := c.(map[string]any)
			keys = append(keys, jStr(cm, "key"))
			labels = append(labels, jStr(cm, "label"))
		}
		var rows [][]string
		for _, r := range jList(data, "rows") {
			if len(rows) == wallRowsMax {
				break
			}
			rm, _ := r.(map[string]any)
			row := make([]string, len(keys))
			for i, k := range keys {
				row[i] = oneLineText(cellText(rm[k]), wallCellTextMax)
			}
			rows = append(rows, row)
		}
		w["columns"], w["rows"] = labels, rows
	case "events":
		var evs []map[string]any
		for _, e := range jList(data, "events") {
			if len(evs) == wallItemsMax {
				break
			}
			em, _ := e.(map[string]any)
			ev := map[string]any{"at": jStr(em, "t"), "actor": refLabel(jObj(em, "actor")), "action": jStr(em, "action")}
			if t := refLabel(jObj(em, "target")); t != "" {
				ev["target"] = t
			}
			if o := jStr(em, "outcome"); o != "" {
				ev["outcome"] = o
			}
			evs = append(evs, ev)
		}
		w["events"] = evs
	case "timeline":
		var items []map[string]any
		for _, e := range jList(data, "events") {
			if len(items) == wallItemsMax {
				break
			}
			em, _ := e.(map[string]any)
			it := map[string]any{"at": jStr(em, "t"), "label": oneLineText(jStr(em, "label"), 160)}
			if tone := jStr(em, "tone"); tone != "" {
				it["tone"] = tone
			}
			items = append(items, it)
		}
		w["items"] = items
	case "geo":
		var pts []map[string]any
		for _, p := range jList(data, "points") {
			if len(pts) == wallItemsMax {
				break
			}
			pm, _ := p.(map[string]any)
			// The catalog's key is place; a data-only widget an agent wrote
			// by hand may have named it otherwise.
			pt := map[string]any{"place": firstStr(pm, "place", "name", "region", "label", "id")}
			for _, key := range []string{"label", "unit", "tone"} {
				if s := jStr(pm, key); s != "" {
					pt[key] = s
				}
			}
			if v, ok := jNum(pm, "value"); ok {
				pt["value"] = v
			}
			pts = append(pts, pt)
		}
		w["points"] = pts
	case "codeFinding":
		ref := jObj(data, "codeRef")
		w["repo"] = jStr(ref, "owner") + "/" + jStr(ref, "repo")
		if p := jStr(ref, "path"); p != "" {
			w["path"] = p
		}
		if l := jStr(ref, "permalink"); l != "" {
			w["permalink"] = l
		}
		snip := jObj(data, "snippet")
		if start, ok := jNum(snip, "startLine"); ok {
			w["startLine"] = int64(start)
		}
		var lines []string
		for _, l := range jList(snip, "lines") {
			if s, ok := l.(string); ok && len(lines) < wallLinesMax {
				lines = append(lines, s)
			}
		}
		w["lines"] = lines
	}
	return w
}

// cellText is a table cell as text: a string, a number, or {v, tone}.
func cellText(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case float64:
		return strconv.FormatFloat(c, 'f', -1, 64)
	case map[string]any:
		return cellText(c["v"])
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

// refLabel names an entity or actor reference by its label, else its id.
func refLabel(m map[string]any) string {
	if l := jStr(m, "label"); l != "" {
		return l
	}
	return jStr(m, "id")
}

// thinPairs keeps at most n points, evenly spaced, first and last kept.
func thinPairs(pts [][2]float64, n int) [][2]float64 {
	if len(pts) <= n || n < 2 {
		return pts
	}
	out := make([][2]float64, 0, n)
	step := float64(len(pts)-1) / float64(n-1)
	for i := 0; i < n; i++ {
		out = append(out, pts[int(float64(i)*step+0.5)])
	}
	return out
}
