package narrate

import (
	"testing"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

func i64(n int64) *int64     { return &n }
func f64(n float64) *float64 { return &n }
func intp(n int) *int        { return &n }
func ev(s int64, typ string, p map[string]any) client.Event {
	return client.Event{Seq: i64(s), Type: typ, Payload: p}
}

func TestVotesOf(t *testing.T) {
	att := &client.Attention{VotesAwaited: []client.VoteAwaited{
		{ClaimSeq: i64(212), Class: "finding", Statement: "5xx\nstarts at 15:45Z", AuthoredBy: "bob-codex", AuthorIsAgent: true,
			PositionsSoFar: 1, ExpiresInMs: f64(250000), Shortfall: &client.Shortfall{Missing: &client.Missing{Corroborators: intp(1)}}},
		{ClaimSeq: i64(214), Statement: "dns is fine", AuthoredBy: "Carol", ExpiresInMs: f64(1000)},
		{ClaimSeq: nil, Statement: "no seq"},
	}}
	claims := &client.ClaimsProjection{Claims: []client.ClaimView{
		{Seq: 212, Author: client.ClaimActor{HumanActorID: "u-bob", Kind: "agent"},
			Provenance: []client.ClaimProvenance{{SourceType: "telemetry", Quote: "CloudWatch 5xxErrorRate"}, {SourceType: "chat", SourceSeq: i64(9)}},
			Outcome:    &client.ClaimOutcome{RequiredBar: &client.ClaimBar{Corroborators: 3}}},
		{Seq: 214, Author: client.ClaimActor{HumanActorID: "u-me", Kind: "member"}},
	}}
	parts := []client.Participant{
		{DisplayName: "bob", HumanActorID: "u-bob", AgentInstanceID: "i-1", EdgeAgentLabel: "bob-codex"},
		{DisplayName: "Bob Builder", HumanActorID: "u-bob", AgentInstanceID: "web:u-bob:t"},
	}
	cases := []struct {
		name    string
		claims  *client.ClaimsProjection
		elapsed float64
		check   func(t *testing.T, got []Vote)
	}{
		{"enriched from the claims projection", claims, 0, func(t *testing.T, got []Vote) {
			if len(got) != 2 {
				t.Fatalf("votes = %+v", got)
			}
			v := got[0]
			if v.Statement != "5xx starts at 15:45Z" || v.AuthorHuman != "Bob Builder" || *v.Needed != 3 || *v.Shortfall != 1 || *v.ExpiresInMs != 250000 {
				t.Fatalf("vote = %+v", v)
			}
			if v.Evidence != "CloudWatch 5xxErrorRate · chat #9" || v.Mine {
				t.Fatalf("evidence %q mine %v", v.Evidence, v.Mine)
			}
			if !got[1].Mine || got[1].Needed != nil || got[1].Evidence != "" {
				t.Fatalf("second = %+v", got[1])
			}
		}},
		{"needed falls back to positions plus shortfall", nil, 0, func(t *testing.T, got []Vote) {
			if *got[0].Needed != 2 || got[0].AuthorHuman != "" {
				t.Fatalf("vote = %+v", got[0])
			}
		}},
		{"time left counts down between reads", nil, 2000, func(t *testing.T, got []Vote) {
			if *got[0].ExpiresInMs != 248000 || got[0].Stale {
				t.Fatalf("first = %+v", got[0])
			}
			if *got[1].ExpiresInMs != -1000 || !got[1].Stale {
				t.Fatalf("second = %+v", got[1])
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.check(t, VotesOf(att, c.claims, parts, "u-me", c.elapsed)) })
	}
	if VotesOf(nil, claims, parts, "u-me", 0) != nil {
		t.Fatal("no attention, no votes")
	}
}

func TestLatestByPerson(t *testing.T) {
	claims := &client.ClaimsProjection{Claims: []client.ClaimView{
		{Seq: 10, Author: client.ClaimActor{HumanActorID: "u-a"}, Statement: "old", State: "admitted", At: "2026-10-08T15:00:00Z"},
		{Seq: 20, Author: client.ClaimActor{HumanActorID: "u-a"}, Statement: "newer", State: "staged", At: "2026-10-08T15:10:00Z",
			Positions: []client.ClaimPosition{{Position: "corroborate", Actor: client.ClaimActor{HumanActorID: "u-a"}}}},
		{Seq: 21, Author: client.ClaimActor{HumanActorID: "u-b"}, Statement: "b says", State: "staged",
			Positions: []client.ClaimPosition{{Position: "corroborate", Actor: client.ClaimActor{HumanActorID: "u-c"}}, {Position: "contest", Actor: client.ClaimActor{HumanActorID: "u-d"}}}},
		{Seq: 22, Author: client.ClaimActor{HumanActorID: "u-c"}, State: "withdrawn"},
		{Seq: 23, Author: client.ClaimActor{HumanActorID: "u-d"}, Statement: "d says", State: "staged",
			Positions: []client.ClaimPosition{{Position: "corroborate", Actor: client.ClaimActor{HumanActorID: "u-a"}}}},
	}}
	events := []client.Event{
		ev(30, "edge.finding", map[string]any{"humanActorId": "u-b", "text": "a note from b", "at": "2026-10-08T15:20:00Z"}),
		ev(5, "edge.finding", map[string]any{"humanActorId": "u-d", "text": "older note"}),
		ev(31, "chat.message", map[string]any{"humanActorId": "u-a", "text": "chat is not shared context"}),
	}
	now := int64(1791473400000) // 2026-10-08T15:30:00Z
	got := LatestByPerson(claims, events, now)
	cases := []struct {
		who, state, text string
		seq              int64
	}{
		{"u-a", "staged", "newer", 20},
		{"u-b", "note", "a note from b", 30},
		{"u-c", "withdrawn", "Withdrew a finding", 22},
		{"u-d", "corroborated", "d says", 23},
	}
	for _, c := range cases {
		l := got[c.who]
		if l == nil || l.State != c.state || l.Text != c.text || l.Seq != c.seq {
			t.Errorf("%s latest = %+v, want %s %q #%d", c.who, l, c.state, c.text, c.seq)
		}
	}
	if got["u-a"].AgeMs == nil || *got["u-a"].AgeMs != 20*60*1000 {
		t.Errorf("u-a age = %v", got["u-a"].AgeMs)
	}
	if got["u-b"].AgeMs == nil || *got["u-b"].AgeMs != 10*60*1000 {
		t.Errorf("u-b age = %v", got["u-b"].AgeMs)
	}
	if contested := LatestByPerson(&client.ClaimsProjection{Claims: claims.Claims[2:3]}, nil, 0)["u-b"]; contested.State != "contested" || contested.AgeMs != nil {
		t.Errorf("contested = %+v", contested)
	}
}

func TestBeaconStepOf(t *testing.T) {
	events := []client.Event{
		ev(1, "agent.run.started", nil),
		ev(2, "agent.step", map[string]any{"text": "old run", "runSeq": float64(0)}),
		ev(3, "agent.step", map[string]any{"text": "one", "runSeq": float64(1)}),
		ev(4, "agent.step", map[string]any{"text": "two", "runSeq": float64(1)}),
	}
	s := BeaconStepOf(events)
	if s == nil || s.Text != "two" || s.Step != 2 || s.RunSeq != 1 {
		t.Fatalf("step = %+v", s)
	}
	if BeaconStepOf(events[:1]) != nil {
		t.Fatal("no step, nil")
	}
	// A status only carries it while the run is live.
	st := StatusOfRoom(nil, "", RoomReads{Events: append(events, ev(5, "agent.run.concluded", nil))})
	if st.BeaconStep != nil || st.Beacon != "concluded" {
		t.Fatalf("concluded status = %+v", st)
	}
	if st := StatusOfRoom(nil, "", RoomReads{Events: events}); st.BeaconStep == nil {
		t.Fatal("a live run carries its step")
	}
}

func TestBeaconConclusionOf(t *testing.T) {
	cases := []struct {
		name   string
		events []client.Event
		want   *BeaconConclusion
	}{
		{"no conclusion", []client.Event{ev(1, "agent.run.started", nil)}, nil},
		{"the concluded row's own text", []client.Event{ev(1, "agent.run.concluded", map[string]any{"summary": "it was dns"})}, &BeaconConclusion{Text: "it was dns", Seq: 1}},
		{"the run's newest hypothesis before its findings", []client.Event{
			ev(1, "agent.hypothesis.raised", map[string]any{"statement": "previous run"}),
			ev(2, "agent.run.started", nil),
			ev(3, "agent.hypothesis.raised", map[string]any{"statement": "origin-b fails"}),
			ev(4, "agent.finding", map[string]any{"text": "a finding"}),
			ev(5, "agent.run.concluded", map[string]any{"ok": true}),
		}, &BeaconConclusion{Text: "origin-b fails", Seq: 5}},
		{"a finding when the run raised no hypothesis", []client.Event{
			ev(2, "agent.run.started", nil),
			ev(4, "agent.finding", map[string]any{"text": "a finding"}),
			ev(5, "agent.run.concluded", map[string]any{"ok": true}),
		}, &BeaconConclusion{Text: "a finding", Seq: 5}},
		{"nothing said", []client.Event{ev(2, "agent.run.started", nil), ev(5, "agent.run.concluded", map[string]any{"ok": true})}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := BeaconConclusionOf(c.events)
			if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestScopeAndLines(t *testing.T) {
	scope := ScopeOf([]client.FrameAttachment{{Kind: "component", Label: "web-edge"}, {Kind: "window", Label: "13:00Z-17:00Z"}, {Kind: "repo", Label: ""}})
	if len(scope) != 2 || scope[0] != "component:web-edge" || scope[1] != "window:13:00Z-17:00Z" {
		t.Fatalf("scope = %v", scope)
	}
	lines := LinesOf([]client.LineClaim{
		{ClaimID: "c1", LineKey: "eu-west-1-5xx", Label: "eu-west-1 5xx", Holder: client.LineHolder{HumanActorID: "u-me", DisplayName: "Me"}, ClaimedAtSeq: 7, State: "held"},
		{ClaimID: "c2", LineKey: "dns", Holder: client.LineHolder{HumanActorID: "u-x"}, State: "held"},
		{ClaimID: "c3", LineKey: "gone", State: "released"},
	}, "u-me", []client.Event{ev(7, "line.claimed", map[string]any{"at": "2026-10-08T15:29:00Z"})}, 1791473400000)
	if len(lines) != 2 || !lines[0].You || lines[0].AgeMs == nil || *lines[0].AgeMs != 60000 {
		t.Fatalf("lines = %+v", lines)
	}
	if lines[1].Label != "dns" || lines[1].Owner != "Participant" || lines[1].AgeMs != nil || lines[1].You {
		t.Fatalf("second line = %+v", lines[1])
	}
}
