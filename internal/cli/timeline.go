package cli

// timeline.go — `landfall timeline --room <k> [--before <seq>] [--limit 50]
// [--kind findings|status|beacon|people|all]` (contract §4, review #9).
//
// The room's timeline read with the room's own session through the daemon
// (GET …/events?limit&beforeSeq). The route has no type filter, so --kind is
// applied here: the command pages backwards until it has `limit` rows of that
// kind or reaches the start of the timeline, at most timelinePagesMax pages
// per call. hasMore says whether older rows of that kind may exist: true when
// rows were left over or the start was not reached; false only when the
// start was read.
//
// Bookkeeping rows (widget build steps, ticket redeems, layout saves,
// presence) are left out of every view: they are how the room works, not what
// happened in it. Each Beacon run's agent.step rows fold into the latest one.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/hooks"
	"github.com/landfalls-ai/landfall-cli/internal/narrate"
	"github.com/spf13/cobra"
)

const (
	timelinePagesMax     = 5
	timelineDefaultLimit = 50
	timelineMaxLimit     = 200
	// timelineScanPage is how many rows one page asks for when filtering.
	timelineScanPage = 200
	timelineTextMax  = 240
)

// timelineEvent is one row of GET …/events as the server sends it.
type timelineEvent struct {
	Seq        *int64         `json:"seq"`
	Type       string         `json:"type"`
	ActorType  string         `json:"actorType,omitempty"`
	Payload    map[string]any `json:"payload,omitempty"`
	OccurredAt string         `json:"occurredAt,omitempty"`
}

// TimelineOptions are the command's flags.
type TimelineOptions struct {
	Room   string
	Before int64 // -1: from the newest
	Limit  int
	Kind   string
}

func newTimelineCommand(ui *UI) *cobra.Command {
	var before, limit int
	var kind string
	c := newReadCommand(ui, "timeline", func(cmd *cobra.Command, ws hooks.Workspace, room string) map[string]any {
		return RunTimeline(cmdContext(cmd), TimelineOptions{Room: room, Before: int64(before), Limit: limit, Kind: kind}, defaultReadDeps(ws))
	})
	c.Flags().IntVar(&before, "before", -1, "only rows older than this seq")
	c.Flags().IntVar(&limit, "limit", timelineDefaultLimit, "how many rows")
	c.Flags().StringVar(&kind, "kind", "all", "findings, status, beacon, people or all")
	return c
}

// timelineKinds are the --kind values.
var timelineKinds = map[string]bool{"all": true, "findings": true, "status": true, "beacon": true, "people": true, "other": true}

// bookkeeping rows are left out of every view.
var bookkeeping = map[string]bool{
	"agent.widget.codegen":    true,
	"agent.widget.signed":     true,
	"agent.widget.validated":  true,
	"agent.widget.rendered":   true,
	"agent.widget.refreshed":  true,
	"agent.widget.executed":   true,
	"edge.ticket.redeemed":    true,
	"canvas.layout.saved":     true,
	"presence.updated":        true,
	"edge.heartbeat":          true,
	"governance.pull":         true,
	"context.pull.recorded":   true,
	"dashboard.version":       true,
	"dashboard.version.saved": true,
}

// eventKind is the contract's bucket for an event type.
func eventKind(t string) string {
	switch {
	case strings.HasPrefix(t, "claim."), t == "edge.finding", t == "agent.finding", t == "agent.hypothesis.raised", strings.HasPrefix(t, "context.flagged"), t == "context.voted":
		return "findings"
	case strings.HasPrefix(t, "incident."), t == "status.changed", t == "severity.changed", t == "severity.proposed", t == "stage.changed", t == "trigger.recovered":
		return "status"
	case strings.HasPrefix(t, "agent.run."), t == "agent.step":
		return "beacon"
	case strings.HasPrefix(t, "edge.participant."), t == "chat.message", t == "chat.reaction":
		return "people"
	}
	return "other"
}

// RunTimeline builds the timeline answer.
func RunTimeline(ctx context.Context, o TimelineOptions, d ReadDeps) map[string]any {
	kind := strings.ToLower(strings.TrimSpace(o.Kind))
	if kind == "" {
		kind = "all"
	}
	if !timelineKinds[kind] {
		return failure("Pick a kind: findings, status, beacon, people or all.")
	}
	limit := o.Limit
	if limit <= 0 {
		limit = timelineDefaultLimit
	}
	if limit > timelineMaxLimit {
		limit = timelineMaxLimit
	}
	room, why := pickRoom(d, o.Room)
	if why != "" {
		return failure(why)
	}

	// Newest first while scanning backwards; reversed at the end.
	var kept []timelineEvent
	before := o.Before
	reachedStart := false
	pageSize := limit
	if kind != "all" || pageSize < 50 {
		pageSize = timelineScanPage
	}
	for page := 0; page < timelinePagesMax && len(kept) < limit+1; page++ {
		path := "/events?limit=" + strconv.Itoa(pageSize)
		if before >= 0 {
			path += "&beforeSeq=" + strconv.FormatInt(before, 10)
		}
		raw, err := d.RoomRead(ctx, room.RoomKey, path)
		if err != nil {
			return failure(roomRefusal(err, "the timeline"))
		}
		var events []timelineEvent
		if err := json.Unmarshal(raw, &events); err != nil {
			return failure("Landfall sent a timeline this CLI could not read.")
		}
		oldest := int64(-1)
		for i := len(events) - 1; i >= 0; i-- {
			e := events[i]
			if e.Seq == nil {
				continue
			}
			if oldest < 0 || *e.Seq < oldest {
				oldest = *e.Seq
			}
			if bookkeeping[e.Type] {
				continue
			}
			if kind != "all" && eventKind(e.Type) != kind {
				continue
			}
			kept = append(kept, e)
		}
		if len(events) < pageSize || oldest <= 0 {
			reachedStart = true
			break
		}
		before = oldest
	}

	rows := foldSteps(kept)
	hasMore := !reachedStart
	if len(rows) > limit {
		rows = rows[:limit]
		hasMore = true
	}
	// Oldest first within the page.
	out := make([]map[string]any, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		out = append(out, timelineRow(rows[i]))
	}
	ans := map[string]any{"ok": true, "events": out, "hasMore": hasMore}
	if len(out) > 0 {
		ans["oldestSeq"] = out[0]["seq"]
	}
	return ans
}

// foldedEvent is a row with how many agent.step rows it stands for.
type foldedEvent struct {
	timelineEvent
	steps int
}

// foldSteps keeps, for each Beacon run, only its newest agent.step (the
// input is newest first), counting the ones it stands for.
func foldSteps(newestFirst []timelineEvent) []foldedEvent {
	out := make([]foldedEvent, 0, len(newestFirst))
	stepAt := -1 // index in out of the newest step of the current run
	for _, e := range newestFirst {
		switch {
		case e.Type == "agent.step":
			if stepAt >= 0 {
				out[stepAt].steps++
				continue
			}
			out = append(out, foldedEvent{timelineEvent: e, steps: 1})
			stepAt = len(out) - 1
		case strings.HasPrefix(e.Type, "agent.run."):
			// Older rows belong to another run.
			out = append(out, foldedEvent{timelineEvent: e})
			stepAt = -1
		default:
			out = append(out, foldedEvent{timelineEvent: e})
		}
	}
	return out
}

// timelineRow is one row as the contract shapes it.
func timelineRow(e foldedEvent) map[string]any {
	ev := narrateEvent(e.timelineEvent)
	glyph, tone := eventGlyph(e.timelineEvent)
	text := narrate.EventTextOf(ev)
	if text == "" {
		text = eventFallbackText(e.timelineEvent)
	}
	// A refused signal read carries the server's reason code ("no-live-grant")
	// as its only text; that code is not a sentence a person should read.
	if e.Type == "edge.signals.denied" {
		text = "a signal read was refused"
	}
	who := narrate.EventActor(e.Payload)
	if who == "" {
		who = eventSource(e.timelineEvent)
	}
	detail := ""
	if e.Type == "agent.step" {
		if n, ok := jNum(e.Payload, "step"); ok {
			detail = fmt.Sprintf("step %d", int64(n))
		}
		if e.steps > 1 {
			if detail != "" {
				detail += ", "
			}
			detail += fmt.Sprintf("latest of %d steps", e.steps)
		}
	}
	return map[string]any{
		"seq":    e.SeqOrZero(),
		"at":     e.OccurredAt,
		"kind":   eventKind(e.Type),
		"type":   e.Type,
		"glyph":  glyph,
		"tone":   tone,
		"text":   oneLineText(narrate.Printable(text), timelineTextMax),
		"who":    oneLineText(narrate.Printable(who), 60),
		"detail": detail,
	}
}

// SeqOrZero is the event's seq, 0 when it has none (never kept: rows without
// a seq are skipped while scanning).
func (e timelineEvent) SeqOrZero() int64 {
	if e.Seq == nil {
		return 0
	}
	return *e.Seq
}

func narrateEvent(e timelineEvent) client.Event {
	return client.Event{Seq: e.Seq, Type: e.Type, Payload: e.Payload, ActorType: e.ActorType}
}

// eventGlyph is a row's mark and tone (kit.js TONE).
func eventGlyph(e timelineEvent) (string, string) {
	switch e.Type {
	case "incident.triggered", "incident.opened":
		return "▲", "critical"
	case "severity.changed", "severity.proposed":
		return "▲", "serious"
	case "status.changed":
		if closedStatus(jStr(e.Payload, "status")) {
			return "✓", "good"
		}
		return "●", "warning"
	case "trigger.recovered":
		return "✓", "good"
	case "stage.changed":
		return "◆", "info"
	case "claim.admitted":
		return "◆", "good"
	case "claim.corroborated":
		return "✓", "good"
	case "claim.contested":
		return "✕", "warning"
	case "claim.withdrawn", "claim.expired":
		return "○", "neutral"
	case "claim.staged", "edge.finding", "agent.finding", "agent.hypothesis.raised":
		return "◇", "info"
	case "agent.run.started", "agent.step":
		return "◎", "violet"
	case "agent.run.concluded":
		return "◉", "violet"
	case "agent.run.gaveup":
		return "○", "neutral"
	case "chat.message", "chat.reaction":
		return "›", "neutral"
	case "edge.participant.joined":
		return "+", "neutral"
	case "edge.participant.left":
		return "−", "neutral"
	}
	if strings.HasPrefix(e.Type, "incident.") {
		return "●", "warning"
	}
	return "·", "neutral"
}

// eventSource names who wrote a row that carries no display name: the alert
// source for a trigger, Beacon for its own run, else the server's actor type.
func eventSource(e timelineEvent) string {
	if s := jStr(e.Payload, "source"); s != "" && (e.Type == "incident.triggered" || e.Type == "trigger.recovered" || e.Type == "severity.proposed") {
		return s
	}
	if strings.HasPrefix(e.Type, "agent.") {
		return "Beacon"
	}
	if e.ActorType == "system" {
		return "Landfall"
	}
	return ""
}

// eventFallbackText says what a row with no text of its own is.
func eventFallbackText(e timelineEvent) string {
	switch e.Type {
	case "agent.run.started":
		return "Beacon started investigating"
	case "agent.run.concluded":
		return "Beacon concluded"
	case "agent.run.gaveup":
		return "Beacon gave up"
	case "edge.participant.joined":
		return "joined the room"
	case "edge.participant.left":
		return "left the room"
	case "severity.changed":
		if s := jStr(e.Payload, "severity"); s != "" {
			return "severity " + s
		}
	case "stage.changed":
		if s := jStr(e.Payload, "stage"); s != "" {
			return "stage " + s
		}
	case "incident.triggered":
		if s := jStr(e.Payload, "title"); s != "" {
			return s
		}
	}
	return strings.ReplaceAll(e.Type, ".", " ")
}
