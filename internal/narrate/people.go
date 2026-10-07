package narrate

import (
	"sort"
	"strconv"
	"strings"

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
}

// Person is one human in the room with their agents. Here is true while any
// of their browser tabs or agents is active; Browser while a tab is.
type Person struct {
	Name    string  `json:"name"`
	You     bool    `json:"you,omitempty"`
	Here    bool    `json:"here"`
	Browser bool    `json:"browser,omitempty"`
	Agents  []Agent `json:"agents,omitempty"`
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
	st.People = peopleOf(f.Participants, me)
	st.Theory = theoryOf(f.Brief)
	return st
}

func peopleOf(parts []client.Participant, me string) []Person {
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
