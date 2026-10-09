package cli

// wallperson.go — `landfall wall --person <humanActorId|me>` and the people
// row every wall answer carries (CONSOLE-SPEC §9, §4.2, §4.3.1): one
// colleague's investigation as the web's person view folds it
// (WarRoomPage.tsx `sub`, CanvasPanel.tsx SubInvestigation), all of it from
// the timeline the wall already reads, with the room's session.
//
//   - widgets: their `edge.widget` snapshots (warroom-ui projectEdgeWidgets).
//     Data-only, so nothing is re-executed and nobody's credentials are
//     involved. The same contributor and the same title supersede: the latest
//     is the card, the older ones ride along as `versions`, newest first. A
//     snapshot with no data is left out, as the web leaves it out. Order: the
//     latest canvas.layout.saved with scope `sub:<humanActorId>` (warroom-ui
//     sharedLayout.ts latestSharedLayout with that scope), else build order
//     (each card by its latest version's seq).
//   - trail: their edge.finding (a finding or a note), edge.hypothesis,
//     edge.query and edge.action.proposed rows, newest first. A finding or a
//     hypothesis that went through the admission gate carries its claim's
//     state, folded from the claim events the way the watch's latest.state is
//     (narrate claimState): admitted and withdrawn stand; otherwise contested
//     when someone outside the author's side contests, corroborated when
//     someone does, else staged.
//   - artifacts: their artifact.shared rows, in `landfall artifacts`' form.
//
// `me` is this machine's person: the humanActorId the room's session was
// issued for, the same id the frame's You is read from.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/daemon"
	"github.com/landfalls-ai/landfall-cli/internal/hooks"
	"github.com/landfalls-ai/landfall-cli/internal/narrate"
)

const (
	// wallTrailMax bounds a person's trail; the console shows eight.
	wallTrailMax = 100
	trailTextMax = 240
)

// WallOptions are `landfall wall`'s arguments.
type WallOptions struct {
	Room string
	// Person is a humanActorId, or "me"; empty answers the shared wall.
	Person string
	// Me is this machine's person in a room (the session's humanActorId).
	Me func(ctx context.Context, roomKey string) (string, error)
}

// defaultMe asks the room daemon for the room's session and reads the
// humanActorId it was issued for.
func defaultMe(ws hooks.Workspace) func(context.Context, string) (string, error) {
	sock := hooks.DaemonSocketPath(ws)
	return func(_ context.Context, roomKey string) (string, error) {
		res, err := daemon.Send(sock, daemon.Request{Op: "session", RoomKey: roomKey}, time.Second)
		if err != nil || res == nil || res.Room == nil {
			return "", errors.New("no session")
		}
		return res.Room.HumanActorID, nil
	}
}

// edgeWidget is one card of a person's dashboard, with its older versions.
type edgeWidget struct {
	projectedWidget
	humanActorID string
	seq          int64
	capturedAt   string
	versions     []edgeWidgetVersion
}

type edgeWidgetVersion struct {
	seq        int64
	capturedAt string
}

// projectEdgeWidgets is warroom-ui projectEdgeWidgets: every edge.widget row
// (one person's, when named), same contributor and title superseded by the
// latest, ordered by each card's latest seq.
func projectEdgeWidgets(events []timelineEvent, humanActorID string) []*edgeWidget {
	groups := map[string][]*edgeWidget{}
	var keys []string
	for _, e := range events {
		if e.Type != "edge.widget" {
			continue
		}
		p := e.Payload
		who := jStr(p, "humanActorId")
		if humanActorID != "" && who != humanActorID {
			continue
		}
		data := jObj(p, "data")
		status := "rendered"
		if len(data) == 0 {
			status = "no-data"
		}
		typ := jStr(p, "widgetType")
		if typ == "" {
			typ = "stat"
		}
		title := jStr(p, "title")
		if title == "" {
			title = "Untitled widget"
		}
		captured := jStr(p, "capturedAt")
		if captured == "" {
			captured = e.OccurredAt
		}
		w := &edgeWidget{
			projectedWidget: projectedWidget{id: fmt.Sprintf("edge-widget-%d", e.SeqOrZero()), typ: typ, title: title, status: status, data: data, landedSeq: e.SeqOrZero()},
			humanActorID:    who,
			seq:             e.SeqOrZero(),
			capturedAt:      captured,
		}
		key := who + "::" + title
		if _, ok := groups[key]; !ok {
			keys = append(keys, key)
		}
		groups[key] = append(groups[key], w)
	}
	out := make([]*edgeWidget, 0, len(keys))
	for _, k := range keys {
		list := groups[k]
		latest := list[len(list)-1]
		for i := len(list) - 2; i >= 0; i-- {
			latest.versions = append(latest.versions, edgeWidgetVersion{seq: list[i].seq, capturedAt: list[i].capturedAt})
		}
		out = append(out, latest)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].seq < out[j].seq })
	return out
}

// renderedEdgeWidgets is what the web draws of a person's dashboard: the
// cards with data.
func renderedEdgeWidgets(events []timelineEvent, humanActorID string) []*edgeWidget {
	var out []*edgeWidget
	for _, w := range projectEdgeWidgets(events, humanActorID) {
		if w.status == "rendered" {
			out = append(out, w)
		}
	}
	return out
}

// layoutOrder is the latest canvas.layout.saved for scope: its order and who
// saved it (warroom-ui latestSharedLayout).
func layoutOrder(events []timelineEvent, scope string) (order []string, by string, found bool) {
	best := int64(-1)
	for _, e := range events {
		if e.Type != "canvas.layout.saved" || e.Seq == nil || *e.Seq <= best {
			continue
		}
		if jStr(e.Payload, "scope") != scope {
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
		best, order, by, found = *e.Seq, ids, jStr(e.Payload, "displayName"), true
	}
	return order, by, found
}

// trailTypes are the rows of a person's trail, and the kind each reads as.
var trailTypes = map[string]string{
	"edge.finding":         "finding",
	"edge.hypothesis":      "hypothesis",
	"edge.query":           "query",
	"edge.action.proposed": "suggestion",
}

// claimStates folds the claim events into each gated contribution's state,
// keyed by the edge row's seq (a claim's provenance points at it).
func claimStates(events []timelineEvent) map[int64]string {
	type claim struct {
		author    string
		state     string // staged, admitted, withdrawn
		positions map[string]string
	}
	claims := map[int64]*claim{}
	bySource := map[int64]int64{}
	ordered := append([]timelineEvent(nil), events...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].SeqOrZero() < ordered[j].SeqOrZero() })
	for _, e := range ordered {
		if e.Seq == nil || !strings.HasPrefix(e.Type, "claim.") {
			continue
		}
		p := e.Payload
		if e.Type == "claim.staged" {
			claims[*e.Seq] = &claim{author: jStr(p, "humanActorId"), state: "staged", positions: map[string]string{}}
			for _, pv := range jList(p, "provenance") {
				pm, _ := pv.(map[string]any)
				if src, ok := jNum(pm, "sourceSeq"); ok {
					if _, taken := bySource[int64(src)]; !taken {
						bySource[int64(src)] = *e.Seq
					}
				}
			}
			continue
		}
		cs, ok := jNum(p, "claimSeq")
		if !ok {
			continue
		}
		c := claims[int64(cs)]
		if c == nil || c.state == "withdrawn" {
			continue
		}
		switch e.Type {
		case "claim.admitted":
			c.state = "admitted"
		case "claim.demoted":
			c.state = "staged"
		case "claim.withdrawn":
			c.state = "withdrawn"
		case "claim.corroborated", "claim.contested":
			who := jStr(p, "humanActorId")
			if who == "" {
				who = "agent:" + jStr(p, "agentInstanceId")
			}
			if c.author != "" && who == c.author {
				continue // the author's own side never counts
			}
			pos := jStr(p, "position")
			if pos == "" {
				pos = "corroborate"
				if e.Type == "claim.contested" {
					pos = "contest"
				}
			}
			c.positions[who] = pos
		}
	}
	out := map[int64]string{}
	for src, cs := range bySource {
		c := claims[cs]
		state := c.state
		if state == "staged" {
			corroborated := false
			for _, pos := range c.positions {
				if pos == "contest" {
					state = "contested"
					break
				}
				if pos == "corroborate" {
					corroborated = true
				}
			}
			if state == "staged" && corroborated {
				state = "corroborated"
			}
		}
		out[src] = state
	}
	return out
}

// trailText is what one trail row says.
func trailText(e timelineEvent) string {
	p := e.Payload
	switch e.Type {
	case "edge.finding":
		return jStr(p, "text")
	case "edge.hypothesis":
		return jStr(p, "statement")
	case "edge.query":
		s := strings.TrimSpace(jStr(p, "source")) + "/" + strings.TrimSpace(jStr(p, "operation"))
		if r := strings.TrimSpace(jStr(p, "resource")); r != "" {
			s += " · " + r
		}
		return s
	case "edge.action.proposed":
		return jStr(p, "description")
	}
	return narrate.EventText(p)
}

// personTrail is one person's trail, newest first, and how many rows it has.
func personTrail(events []timelineEvent, humanActorID string, states map[int64]string) ([]map[string]any, int) {
	var rows []timelineEvent
	for _, e := range events {
		if _, ok := trailTypes[e.Type]; !ok || e.Seq == nil {
			continue
		}
		if jStr(e.Payload, "humanActorId") != humanActorID {
			continue
		}
		rows = append(rows, e)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].SeqOrZero() > rows[j].SeqOrZero() })
	out := make([]map[string]any, 0, len(rows))
	for _, e := range rows {
		if len(out) == wallTrailMax {
			break
		}
		kind := trailTypes[e.Type]
		if e.Type == "edge.finding" && jStr(e.Payload, "contributionKind") == "note" {
			kind = "note"
		}
		at := e.OccurredAt
		if at == "" {
			at = jStr(e.Payload, "at")
		}
		row := map[string]any{
			"seq":  e.SeqOrZero(),
			"at":   at,
			"type": e.Type,
			"kind": kind,
			"text": oneLineText(narrate.Printable(trailText(e)), trailTextMax),
		}
		if kind == "finding" || kind == "hypothesis" {
			if st, ok := states[e.SeqOrZero()]; ok {
				row["state"] = st
			}
		}
		out = append(out, row)
	}
	return out, len(rows)
}

// wallPerson is one row of the answer's people list.
type wallPerson struct {
	humanActorID, displayName, edgeAgentLabel, kind string
	widgets, trail, artifacts                       int
	latestSeq                                       int64
}

// wallPeople is everyone who has contributed to the room from an edge
// session or shared a file, newest activity first, each with what they have
// built: the members of the console's dashboard selector.
func wallPeople(events []timelineEvent) []*wallPerson {
	byID := map[string]*wallPerson{}
	for _, e := range events {
		if !strings.HasPrefix(e.Type, "edge.") && e.Type != "artifact.shared" {
			continue
		}
		p := e.Payload
		id := jStr(p, "humanActorId")
		if id == "" {
			continue
		}
		w, ok := byID[id]
		if !ok {
			w = &wallPerson{humanActorID: id}
			byID[id] = w
		}
		// The latest word on who they are wins: a person who renamed
		// themselves reads under the new name.
		if s := oneLineText(narrate.Printable(jStr(p, "displayName")), 60); s != "" {
			w.displayName = s
		}
		if s := oneLineText(narrate.Printable(jStr(p, "edgeAgentLabel")), 60); s != "" {
			w.edgeAgentLabel = s
		}
		if s := jStr(p, "kind"); s != "" {
			w.kind = s
		}
		if e.SeqOrZero() > w.latestSeq {
			w.latestSeq = e.SeqOrZero()
		}
		if _, isTrail := trailTypes[e.Type]; isTrail && e.Seq != nil {
			w.trail++
		}
	}
	for _, w := range renderedEdgeWidgets(events, "") {
		if p := byID[w.humanActorID]; p != nil {
			p.widgets++
		}
	}
	for id, p := range byID {
		p.artifacts = len(projectArtifacts(events, id))
	}
	out := make([]*wallPerson, 0, len(byID))
	for _, p := range byID {
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].latestSeq != out[j].latestSeq {
			return out[i].latestSeq > out[j].latestSeq
		}
		return out[i].humanActorID < out[j].humanActorID
	})
	return out
}

func (p *wallPerson) row() map[string]any {
	return map[string]any{
		"humanActorId":   p.humanActorID,
		"displayName":    p.displayName,
		"edgeAgentLabel": p.edgeAgentLabel,
		"kind":           p.kind,
		"widgets":        p.widgets,
		"trail":          p.trail,
		"artifacts":      p.artifacts,
		"latestSeq":      p.latestSeq,
	}
}

func peopleRows(people []*wallPerson) []map[string]any {
	out := make([]map[string]any, 0, len(people))
	for _, p := range people {
		out = append(out, p.row())
	}
	return out
}

// RunWallPerson answers one person's dashboard in a room whose timeline is
// already read.
func runWallPerson(ctx context.Context, room daemon.RoomView, events []timelineEvent, o WallOptions) map[string]any {
	people := wallPeople(events)
	id := strings.TrimSpace(o.Person)
	you := false
	if strings.EqualFold(id, "me") {
		if o.Me == nil {
			return failure("Landfall does not know who you are in this room. Join it again with a fresh share link.")
		}
		me, err := o.Me(ctx, room.RoomKey)
		if err != nil || strings.TrimSpace(me) == "" {
			return failure("Landfall does not know who you are in this room. Join it again with a fresh share link.")
		}
		id, you = strings.TrimSpace(me), true
	} else if o.Me != nil {
		if me, err := o.Me(ctx, room.RoomKey); err == nil && me != "" && me == id {
			you = true
		}
	}

	person := map[string]any{"humanActorId": id, "displayName": "", "edgeAgentLabel": "", "kind": "", "you": you}
	for _, p := range people {
		if p.humanActorID == id {
			person["displayName"], person["edgeAgentLabel"], person["kind"] = p.displayName, p.edgeAgentLabel, p.kind
		}
	}

	cards := renderedEdgeWidgets(events, id)
	order, _, arranged := layoutOrder(events, "sub:"+id)
	byID := map[string]*edgeWidget{}
	plain := make([]*projectedWidget, 0, len(cards))
	for _, c := range cards {
		byID[c.id] = c
		plain = append(plain, &c.projectedWidget)
	}
	plain = arrange(plain, order)
	total := len(plain)
	plain = keepNewest(plain, wallWidgetsMax)
	widgets := make([]map[string]any, 0, len(plain))
	for _, pw := range plain {
		c := byID[pw.id]
		flat := flattenWidget(c.id, c.typ, c.title, c.data)
		flat["seq"] = c.seq
		flat["capturedAt"] = c.capturedAt
		versions := make([]map[string]any, 0, len(c.versions))
		for _, v := range c.versions {
			versions = append(versions, map[string]any{"seq": v.seq, "capturedAt": v.capturedAt})
		}
		if len(versions) > 0 {
			flat["versions"] = versions
		}
		widgets = append(widgets, flat)
	}

	trail, trailTotal := personTrail(events, id, claimStates(events))
	arts := projectArtifacts(events, id)

	ans := map[string]any{
		"ok":          true,
		"person":      person,
		"widgets":     widgets,
		"unavailable": []map[string]any{},
		"trail":       trail,
		"artifacts":   artifactRows(arts),
		"people":      peopleRows(people),
		"arranged":    arranged,
	}
	if total > len(widgets) {
		ans["totalWidgets"] = total
	}
	if trailTotal > len(trail) {
		ans["totalTrail"] = trailTotal
	}
	return ans
}
