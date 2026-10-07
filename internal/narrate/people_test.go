package narrate

import (
	"testing"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

func active(b bool) *bool { return &b }

// TestPeopleGroupsAPersonsTabAndAgents: one human with a browser tab and two
// agents is one person, named by their tab, with both agents under them.
func TestPeopleGroupsAPersonsTabAndAgents(t *testing.T) {
	f := &client.ContextFrame{Participants: []client.Participant{
		{DisplayName: "bob", Kind: "member", HumanActorID: "u-bob", AgentInstanceID: "i-1", EdgeAgentLabel: "bob-codex", Active: active(false)},
		{DisplayName: "Bob Builder", Kind: "member", HumanActorID: "u-bob", AgentInstanceID: "web:u-bob:t1", Active: active(true)},
		{DisplayName: "bob", Kind: "member", HumanActorID: "u-bob", AgentInstanceID: "i-2", EdgeAgentLabel: "bob-claude-code", Active: active(true), Doing: "querying ALB healthy hosts"},
	}}
	st := StatusOf(f, "", "")
	if len(st.People) != 1 {
		t.Fatalf("people = %+v", st.People)
	}
	p := st.People[0]
	if p.Name != "Bob Builder" || !p.Here || !p.Browser || len(p.Agents) != 2 {
		t.Fatalf("person = %+v", p)
	}
	if p.Agents[0].Tool != "Codex" || p.Agents[0].Here || p.Agents[1].Tool != "Claude Code" || p.Agents[1].Doing != "querying ALB healthy hosts" {
		t.Fatalf("agents = %+v", p.Agents)
	}
}

// TestPeopleHereFirstYouFirst: who is here comes before who has gone quiet,
// and you lead the people who are here.
func TestPeopleHereFirstYouFirst(t *testing.T) {
	f := &client.ContextFrame{Participants: []client.Participant{
		{DisplayName: "zed", Kind: "member", HumanActorID: "u-zed", AgentInstanceID: "web:u-zed:t", Active: active(false)},
		{DisplayName: "carol", Kind: "member", HumanActorID: "u-carol", AgentInstanceID: "web:u-carol:t", Active: active(true)},
		{DisplayName: "alice", Kind: "member", HumanActorID: "u-alice", AgentInstanceID: "i-3", EdgeAgentLabel: "alice-claude-code", Active: active(true)},
	}}
	st := StatusOf(f, "u-alice", "")
	got := []string{}
	for _, p := range st.People {
		got = append(got, p.Name)
	}
	if len(got) != 3 || got[0] != "alice" || got[1] != "carol" || got[2] != "zed" || !st.People[0].You {
		t.Fatalf("order = %v (%+v)", got, st.People)
	}
}

// TestUnknownPresenceIsNotHere: an absent `active` is "unknown", which the
// status never shows as here.
// TestAMembersAgentIsNotABrowserTab: the server's kind is the person's
// relationship to the organization, so a member's Claude Code session arrives
// as kind "member". Only a `web:` instance is a browser tab.
func TestAMembersAgentIsNotABrowserTab(t *testing.T) {
	f := &client.ContextFrame{Participants: []client.Participant{
		{DisplayName: "collab-bob", Kind: "member", HumanActorID: "u-bob", AgentInstanceID: "01a1", EdgeAgentLabel: "bob-claude-code", Active: active(true)},
	}}
	p := StatusOf(f, "", "").People[0]
	if p.Browser || len(p.Agents) != 1 || p.Agents[0].Tool != "Claude Code" {
		t.Fatalf("a member's agent read as a tab: %+v", p)
	}
}

func TestUnknownPresenceIsNotHere(t *testing.T) {
	f := &client.ContextFrame{Participants: []client.Participant{{DisplayName: "dave", HumanActorID: "u-dave", Kind: "member"}}}
	if st := StatusOf(f, "", ""); st.People[0].Here {
		t.Fatalf("unknown presence read as here: %+v", st.People[0])
	}
}

// TestStatusTextIsCleanedForATerminal: names, the title's status and an
// agent's activity are server text printed into a terminal.
func TestStatusTextIsCleanedForATerminal(t *testing.T) {
	f := &client.ContextFrame{
		Incident:     client.Incident{Status: "open\x1b[2J"},
		Participants: []client.Participant{{DisplayName: "eve\x1b]0;pwned\x07", HumanActorID: "u-eve", Kind: "guest", AgentInstanceID: "i-4", EdgeAgentLabel: "eve-cursor", Doing: "line one\nline two"}},
	}
	st := StatusOf(f, "", "")
	for _, s := range []string{st.Status, st.People[0].Name, st.People[0].Agents[0].Doing} {
		for _, r := range s {
			if r < 0x20 || r == 0x7f {
				t.Fatalf("control character left in %q", s)
			}
		}
	}
}

// TestTheoryPrefersTheNewestWorkingTheory, then the newest established item.
func TestTheoryPrefersTheNewestWorkingTheory(t *testing.T) {
	b := client.Brief{
		Established:   []client.BriefItem{{Seq: 90, Statement: "healthy hosts fell to 2"}},
		WorkingTheory: []client.BriefItem{{Seq: 10, Statement: "older theory"}, {Seq: 30, Statement: "the deploy"}},
	}
	if got := StatusOf(&client.ContextFrame{Brief: b}, "", "").Theory; got != "the deploy" {
		t.Fatalf("theory = %q", got)
	}
	b.WorkingTheory = nil
	if got := StatusOf(&client.ContextFrame{Brief: b}, "", "").Theory; got != "healthy hosts fell to 2" {
		t.Fatalf("theory = %q", got)
	}
}

func TestBeaconStateIsTheNewestRunEvent(t *testing.T) {
	ev := func(t string) client.Event { return client.Event{Type: t} }
	for _, tc := range []struct {
		events []client.Event
		want   string
	}{
		{nil, ""},
		{[]client.Event{ev("chat.message")}, ""},
		{[]client.Event{ev("agent.run.started"), ev("agent.query")}, "investigating"},
		{[]client.Event{ev("agent.run.started"), ev("agent.run.concluded")}, "concluded"},
		{[]client.Event{ev("agent.run.gaveup"), ev("agent.run.started")}, "investigating"},
		// A run that started before this machine joined: its work says so.
		{[]client.Event{ev("agent.query"), ev("agent.widget.requested")}, "investigating"},
		{[]client.Event{ev("agent.query"), ev("agent.run.concluded"), ev("agent.query")}, "concluded"},
		{[]client.Event{ev("edge.finding"), ev("claim.staged")}, ""},
	} {
		if got := BeaconState(tc.events); got != tc.want {
			t.Fatalf("BeaconState(%v) = %q, want %q", tc.events, got, tc.want)
		}
	}
}

func TestToolName(t *testing.T) {
	for label, want := range map[string]string{
		"alice-claude-code": "Claude Code",
		"codex":             "Codex",
		"bob-cursor":        "Cursor",
		"my-own-thing":      "my-own-thing",
		"":                  "agent",
	} {
		if got := ToolName(label); got != want {
			t.Fatalf("ToolName(%q) = %q, want %q", label, got, want)
		}
	}
}
