package narrate

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

// RoomStatus is the room at a glance, for a front end to draw beside the
// prompt: the incident's status and severity, who is in it, what Beacon is
// doing, and the leading theory. Everything in it is server text cleaned for a
// terminal; nothing in it is news (that is the untold set's job).
type RoomStatus struct {
	Status   string   `json:"status,omitempty"`
	Severity string   `json:"severity,omitempty"`
	People   []Person `json:"people,omitempty"`
	// Beacon is "investigating", "concluded" or "gave up", from the room's own
	// run events; empty when this machine has not seen a run.
	Beacon string `json:"beacon,omitempty"`
	// Theory is the newest working theory, else the newest established item.
	Theory string `json:"theory,omitempty"`
	// Refreshing is set while the daemon is reading the frame again behind
	// this answer, so a watcher can ask again shortly rather than waiting out
	// its tick. Not something to draw.
	Refreshing bool `json:"refreshing,omitempty"`

	// BeaconStep is Beacon's newest agent.step while a run is live.
	BeaconStep *BeaconStep `json:"beaconStep,omitempty"`
	// BeaconConclusion is what the latest concluded run settled on, kept by
	// the daemon after the events that said it have left the ring.
	BeaconConclusion *BeaconConclusion `json:"beaconConclusion,omitempty"`
	// Lines are the lines of investigation someone holds right now.
	Lines []Line `json:"lines,omitempty"`
	// Focus is what a person last steered the investigation toward.
	Focus string `json:"focus,omitempty"`
	// Scope is what the room is pinned to, in short words ("component:web-edge").
	Scope []string `json:"scope,omitempty"`
}

// Latest is a person's newest contribution to the shared context: a claim
// (with where it stands at the gate) or, failing one newer, a note.
type Latest struct {
	Seq  int64  `json:"seq"`
	Text string `json:"text"`
	// State is staged, corroborated, contested, admitted, withdrawn or note.
	State string `json:"state"`
	AgeMs *int64 `json:"ageMs,omitempty"`
}

// BeaconStep is one step of Beacon's live run.
type BeaconStep struct {
	Text   string `json:"text"`
	Step   int    `json:"step"`
	RunSeq int64  `json:"runSeq"`
}

// BeaconConclusion is a concluded run's answer and the seq it concluded at.
type BeaconConclusion struct {
	Text string `json:"text"`
	Seq  int64  `json:"seq"`
}

// Line is one held line of investigation.
type Line struct {
	ClaimID string `json:"claimId"`
	Label   string `json:"label"`
	LineKey string `json:"lineKey"`
	// Owner is the holder's display name.
	Owner string `json:"owner"`
	You   bool   `json:"you,omitempty"`
	AgeMs *int64 `json:"ageMs,omitempty"`
}

// Person is one human in the room with their agents. Here is true while any
// of their browser tabs or agents is active; Browser while a tab is.
type Person struct {
	Name string `json:"name"`
	// HumanActorID is who the person is to the server, so a front end keys a
	// person by id rather than by a name two people can share. Empty only
	// when an older server sent no id.
	HumanActorID string  `json:"humanActorId,omitempty"`
	You          bool    `json:"you,omitempty"`
	Here         bool    `json:"here"`
	Browser      bool    `json:"browser,omitempty"`
	Agents       []Agent `json:"agents,omitempty"`
	// Latest is their newest contribution to the shared context, if any.
	Latest *Latest `json:"latest,omitempty"`
}

// Agent is one agent session a person has in the room.
type Agent struct {
	// Tool is the harness in words ("Claude Code"), else the session's label.
	Tool  string `json:"tool"`
	Label string `json:"label,omitempty"`
	Here  bool   `json:"here"`
	Doing string `json:"doing,omitempty"`
}

// peopleMax bounds what a status carries: a room is a handful of people, and
// the stream is redrawn on every change.
const peopleMax = 24

// textMax bounds every free-text field a person reads in a status.
const textMax = 160

// StatusOf reads a context frame into a RoomStatus. me is this machine's
// person (their humanActorId), marked You. beacon comes from the room's
// events (BeaconState), not the frame. A nil frame gives only Beacon.
func StatusOf(f *client.ContextFrame, me, beacon string) RoomStatus {
	st := RoomStatus{Beacon: beacon}
	if f == nil {
		return st
	}
	st.Status = oneLine(Printable(f.Incident.Status), 40)
	st.Severity = oneLine(Printable(f.Incident.Severity), 40)
	st.People = peopleOf(f.Participants, me, nil)
	st.Theory = theoryOf(f.Brief)
	return st
}

// RoomReads is what the daemon reads beside the frame, as the person: the
// room's events (its ring), the claims projection, the line claims, and the
// conclusion it kept. NowMs is the caller's clock (this package reads none).
type RoomReads struct {
	Events     []client.Event
	Claims     *client.ClaimsProjection
	Lines      []client.LineClaim
	Conclusion *BeaconConclusion
	NowMs      int64
}

// StatusOfRoom is StatusOf plus everything the person reads beside the frame:
// each person's latest contribution, Beacon's live step and last conclusion,
// the held lines, and the room's focus and scope.
func StatusOfRoom(f *client.ContextFrame, me string, r RoomReads) RoomStatus {
	st := StatusOf(f, me, BeaconState(r.Events))
	if f != nil {
		st.People = peopleOf(f.Participants, me, LatestByPerson(r.Claims, r.Events, r.NowMs))
		if f.Focus != nil {
			st.Focus = oneLine(Printable(f.Focus.Focus), textMax)
		}
		st.Scope = ScopeOf(f.Attachments)
	}
	if st.Beacon == "investigating" {
		st.BeaconStep = BeaconStepOf(r.Events)
	}
	st.BeaconConclusion = r.Conclusion
	st.Lines = LinesOf(r.Lines, me, r.Events, r.NowMs)
	return st
}

func peopleOf(parts []client.Participant, me string, latest map[string]*Latest) []Person {
	byID := map[string]*Person{}
	order := []string{}
	for i, p := range parts {
		id := p.HumanActorID
		if id == "" {
			// An older server sends no id: each participant stands alone.
			id = "#" + strconv.Itoa(i)
		}
		person, ok := byID[id]
		if !ok {
			person = &Person{You: me != "" && id == me}
			if p.HumanActorID != "" {
				person.HumanActorID = oneLine(Printable(p.HumanActorID), 120)
			}
			byID[id] = person
			order = append(order, id)
		}
		active := p.Active != nil && *p.Active
		name := oneLine(Printable(p.DisplayName), 60)
		// Kind says who the person is to the organization (member, guest,
		// agent), not where they are: a browser tab is the entry the server
		// keys `web:<who>:<tab>`; everything else is an agent session.
		tab := strings.HasPrefix(p.AgentInstanceID, "web:") || (p.AgentInstanceID == "" && p.EdgeAgentLabel == "")
		if tab {
			if active {
				person.Browser = true
			}
		} else {
			person.Agents = append(person.Agents, Agent{
				Tool:  ToolName(p.EdgeAgentLabel),
				Label: oneLine(Printable(p.EdgeAgentLabel), 60),
				Here:  active,
				Doing: oneLine(Printable(p.Doing), textMax),
			})
		}
		// A tab's name is the person's own; an agent's is a fallback.
		if name != "" && (person.Name == "" || tab) {
			person.Name = name
		}
		if active {
			person.Here = true
		}
	}
	out := make([]Person, 0, len(order))
	for _, id := range order {
		p := byID[id]
		if p.Name == "" {
			p.Name = "Participant"
		}
		if l, ok := latest[id]; ok {
			p.Latest = l
		}
		out = append(out, *p)
	}
	// Who is here first, then by name; you lead your own group.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Here != out[j].Here {
			return out[i].Here
		}
		if out[i].You != out[j].You {
			return out[i].You
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	if len(out) > peopleMax {
		out = out[:peopleMax]
	}
	return out
}

// toolNames maps a seat label's harness suffix to the name a person knows.
var toolNames = []struct{ suffix, name string }{
	{"claude-code", "Claude Code"},
	{"claude-desktop", "Claude"},
	{"codex", "Codex"},
	{"cursor", "Cursor"},
	{"gemini", "Gemini"},
	{"copilot", "Copilot"},
	{"windsurf", "Windsurf"},
	{"edge", "Landfall Edge"},
}

// ToolName is the harness an agent label names ("alice-claude-code" is
// Claude Code), else the label itself.
func ToolName(label string) string {
	l := strings.ToLower(label)
	for _, t := range toolNames {
		if l == t.suffix || strings.HasSuffix(l, "-"+t.suffix) {
			return t.name
		}
	}
	if label == "" {
		return "agent"
	}
	return oneLine(Printable(label), 60)
}

func theoryOf(b client.Brief) string {
	pick := func(items []client.BriefItem) string {
		if len(items) == 0 {
			return ""
		}
		newest := items[0]
		for _, it := range items[1:] {
			if it.Seq > newest.Seq {
				newest = it
			}
		}
		return oneLine(Printable(newest.Statement), textMax)
	}
	if t := pick(b.WorkingTheory); t != "" {
		return t
	}
	return pick(b.Established)
}

// BeaconState reads Beacon's run from the room's events, oldest first: the
// newest run event wins. A run that started before this machine joined has no
// `agent.run.started` here, so Beacon's own work (a query, a step, a widget,
// a finding) also says it is investigating. Empty when there is none.
func BeaconState(events []client.Event) string {
	state := ""
	for _, e := range events {
		switch {
		case e.Type == "agent.run.concluded":
			state = "concluded"
		case e.Type == "agent.run.gaveup":
			state = "gave up"
		case e.Type == "agent.run.started":
			state = "investigating"
		case beaconWork[e.Type] && state == "":
			state = "investigating"
		}
	}
	return state
}

// beaconWork is what Beacon writes while a run is going. Agents that join
// from a terminal write edge.* and claim.* rows, never these.
var beaconWork = map[string]bool{
	"agent.query":                true,
	"agent.step":                 true,
	"agent.finding":              true,
	"agent.hypothesis.raised":    true,
	"agent.remediation.proposed": true,
	"agent.widget.requested":     true,
}

// noteTypes are the ring rows a person's note or finding arrives as when it
// did not go through the gate as a claim (a note is an edge.finding with a
// contributionKind marker).
var noteTypes = map[string]bool{"edge.finding": true, "edge.hypothesis": true}

// LatestByPerson is each person's newest contribution to the shared context,
// keyed by their humanActorId: the newest claim they or their agents authored
// (from the claims projection, which knows where it stands at the gate), or a
// newer note from the ring. A claim's state is staged, corroborated (someone
// other than the author's side agreed), contested (someone disagreed),
// admitted or withdrawn.
func LatestByPerson(claims *client.ClaimsProjection, events []client.Event, nowMs int64) map[string]*Latest {
	out := map[string]*Latest{}
	put := func(id string, l *Latest) {
		if id == "" {
			return
		}
		if have, ok := out[id]; ok && have.Seq >= l.Seq {
			return
		}
		out[id] = l
	}
	if claims != nil {
		for _, c := range claims.Claims {
			text := oneLine(Printable(c.Statement), textMax)
			state := claimState(c)
			if state == "withdrawn" && text == "" {
				text = "Withdrew a finding"
			}
			l := &Latest{Seq: c.Seq, Text: text, State: state}
			if at, ok := parseTimeMs(c.At); ok {
				l.AgeMs = ageMs(nowMs, at)
			}
			put(c.Author.HumanActorID, l)
		}
	}
	for _, e := range events {
		if e.Seq == nil || !noteTypes[e.Type] {
			continue
		}
		id, _ := e.Payload["humanActorId"].(string)
		text := oneLine(Printable(EventText(e.Payload)), textMax)
		if id == "" || text == "" {
			continue
		}
		l := &Latest{Seq: *e.Seq, Text: text, State: "note"}
		if at, ok := EventAtMs(e); ok {
			l.AgeMs = ageMs(nowMs, at)
		}
		put(id, l)
	}
	return out
}

// ClaimState is where a claim stands, in the words a person reads: the same
// state a person's latest contribution carries (`landfall brief` folds each
// open item's state with it).
func ClaimState(c client.ClaimView) string { return claimState(c) }

// claimState is where a claim stands, in the words a person reads.
func claimState(c client.ClaimView) string {
	switch c.State {
	case "admitted", "withdrawn":
		return c.State
	}
	corroborated := false
	for _, p := range c.Positions {
		// A position from the author's own side never counts, so it never
		// moves the claim either way.
		if p.Actor.HumanActorID != "" && p.Actor.HumanActorID == c.Author.HumanActorID {
			continue
		}
		switch p.Position {
		case "contest":
			return "contested"
		case "corroborate":
			corroborated = true
		}
	}
	if corroborated {
		return "corroborated"
	}
	return "staged"
}

// scopeMax bounds how many pinned items the stream carries.
const scopeMax = 12

// ScopeOf is the room's pinned scope in short words: "<kind>:<label>".
func ScopeOf(atts []client.FrameAttachment) []string {
	var out []string
	for _, a := range atts {
		label := oneLine(Printable(a.Label), 60)
		kind := oneLine(Printable(a.Kind), 24)
		if label == "" {
			continue
		}
		item := label
		if kind != "" {
			item = kind + ":" + label
		}
		out = append(out, item)
		if len(out) == scopeMax {
			break
		}
	}
	return out
}

// BeaconStepOf is Beacon's newest agent.step, numbered within its run.
// Nil when the ring holds no step.
func BeaconStepOf(events []client.Event) *BeaconStep {
	var newest *client.Event
	for i := range events {
		if events[i].Type == "agent.step" {
			newest = &events[i]
		}
	}
	if newest == nil {
		return nil
	}
	text := oneLine(Printable(EventText(newest.Payload)), textMax)
	if text == "" {
		return nil
	}
	run := payloadInt(newest.Payload, "runSeq")
	step := 0
	for _, e := range events {
		if e.Type == "agent.step" && payloadInt(e.Payload, "runSeq") == run && e.SeqOr(-1) <= newest.SeqOr(-1) {
			step++
		}
	}
	return &BeaconStep{Text: text, Step: step, RunSeq: run}
}

// BeaconConclusionOf is what the newest concluded run in the ring settled on:
// the concluded row's own text when it carries any, else the run's newest
// hypothesis, else its newest finding. Nil when no run concluded in the ring
// or nothing in it says what the run found.
func BeaconConclusionOf(events []client.Event) *BeaconConclusion {
	end := -1
	for i, e := range events {
		if e.Type == "agent.run.concluded" && e.Seq != nil {
			end = i
		}
	}
	if end < 0 {
		return nil
	}
	seq := *events[end].Seq
	if t := oneLine(Printable(EventText(events[end].Payload)), textMax); t != "" {
		return &BeaconConclusion{Text: t, Seq: seq}
	}
	start := 0
	for i := end - 1; i >= 0; i-- {
		if events[i].Type == "agent.run.started" || events[i].Type == "agent.run.concluded" || events[i].Type == "agent.run.gaveup" {
			start = i + 1
			break
		}
	}
	for _, want := range []string{"agent.hypothesis.raised", "agent.finding"} {
		for i := end - 1; i >= start; i-- {
			if events[i].Type != want {
				continue
			}
			if t := oneLine(Printable(EventText(events[i].Payload)), textMax); t != "" {
				return &BeaconConclusion{Text: t, Seq: seq}
			}
		}
	}
	return nil
}

// linesMax bounds how many held lines the stream carries.
const linesMax = 24

// LinesOf is the held lines, oldest claim first. Age comes from the claim's
// own row when the ring still has it.
func LinesOf(claims []client.LineClaim, me string, events []client.Event, nowMs int64) []Line {
	at := map[int64]int64{}
	for _, e := range events {
		if e.Type == "line.claimed" && e.Seq != nil {
			if ms, ok := EventAtMs(e); ok {
				at[*e.Seq] = ms
			}
		}
	}
	var out []Line
	for _, c := range claims {
		if c.State != "held" {
			continue
		}
		owner := oneLine(Printable(c.Holder.DisplayName), 60)
		if owner == "" {
			owner = "Participant"
		}
		l := Line{
			ClaimID: c.ClaimID,
			Label:   oneLine(Printable(c.Label), 120),
			LineKey: c.LineKey,
			Owner:   owner,
			You:     me != "" && c.Holder.HumanActorID == me,
		}
		if l.Label == "" {
			l.Label = c.LineKey
		}
		if ms, ok := at[c.ClaimedAtSeq]; ok {
			l.AgeMs = ageMs(nowMs, ms)
		}
		out = append(out, l)
		if len(out) == linesMax {
			break
		}
	}
	return out
}

// EventAtMs is when an event happened: its payload's `at`, else the row's own
// occurredAt. False when neither is there or readable.
func EventAtMs(e client.Event) (int64, bool) {
	if s, ok := e.Payload["at"].(string); ok {
		if ms, ok := parseTimeMs(s); ok {
			return ms, true
		}
	}
	if len(e.Raw) > 0 {
		var row struct {
			OccurredAt string `json:"occurredAt"`
		}
		if json.Unmarshal(e.Raw, &row) == nil {
			return parseTimeMs(row.OccurredAt)
		}
	}
	return 0, false
}

func parseTimeMs(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0, false
	}
	return t.UnixMilli(), true
}

// ageMs is now minus then, never negative; nil without a clock.
func ageMs(nowMs, thenMs int64) *int64 {
	if nowMs <= 0 {
		return nil
	}
	d := nowMs - thenMs
	if d < 0 {
		d = 0
	}
	return &d
}

func payloadInt(p map[string]any, key string) int64 {
	switch v := p[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	}
	return -1
}
