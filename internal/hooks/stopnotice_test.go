package hooks

// stopnotice_test.go — the Stop hook's default since 2026-09-28: it tells the
// PERSON the room has news and never starts an agent turn they did not ask for.
//
// The three guarantees under test:
//
//	never blocks        exit 0 on every inform path, no `decision` on the wire
//	consumes nothing    the news is still owed when the person next speaks
//	reaches the agent   UserPromptSubmit hands it over on that next message,
//	                    after which the Stop notice goes quiet

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/narrate"
	"github.com/landfalls-ai/landfall-cli/internal/session"
)

// demoRoom is a session holding what a teammate's agent and a teammate
// published while this person's agent was working.
func demoRoom(t *testing.T) *session.Session {
	t.Helper()
	s := session.New(session.Options{Client: client.New(client.Config{IncidentID: "inc-1", Slug: "landfall"}, nil)})
	one, two := int64(41), int64(42)
	s.EnqueueEvent(context.Background(), client.Event{
		Seq: &one, Type: "edge.finding",
		Payload: map[string]any{"displayName": "Maya", "edgeAgentLabel": "Codex", "text": "origin 5xx began at 14:02"},
	})
	s.EnqueueEvent(context.Background(), client.Event{
		Seq: &two, Type: "chat.message",
		Payload: map[string]any{"displayName": "Jhonny", "text": "rolling back the origin config"},
	})
	return s
}

// liveQuery answers every peek from the session as it is NOW, the way a real
// socket would, so a sequence of hooks sees each other's consumes.
func liveQuery(t *testing.T, s *session.Session) func(SocketRequest) ([]SocketAnswer, error) {
	return func(SocketRequest) ([]SocketAnswer, error) {
		return []SocketAnswer{peekOfSession(t, "/s", s)}, nil
	}
}

func liveConsume(s *session.Session) func(string, SocketRequest) error {
	return func(_ string, req SocketRequest) error {
		upTo, _ := req.UpTo.(int64)
		if f, ok := req.UpTo.(float64); ok {
			upTo = int64(f)
		}
		s.ConsumeUpTo(upTo)
		return nil
	}
}

// --- the mode switch ---------------------------------------------------------

func TestInformIsTheDefaultAndOnlyAnExplicitOptInBlocks(t *testing.T) {
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == StopModeEnv {
				return v
			}
			return ""
		}
	}
	for _, v := range []string{"", "inform", "blok", "true", "1", "yes"} {
		if got := ResolveStopMode(false, env(v)); got != StopModeInform {
			t.Fatalf("%s=%q must read as inform, got %q", StopModeEnv, v, got)
		}
	}
	for _, v := range []string{"block", "BLOCK", " Block "} {
		if got := ResolveStopMode(false, env(v)); got != StopModeBlock {
			t.Fatalf("%s=%q must opt in to block, got %q", StopModeEnv, v, got)
		}
	}
	if got := ResolveStopMode(true, env("")); got != StopModeBlock {
		t.Fatalf("--block must opt in, got %q", got)
	}
	// The handler's zero value is inform too, so a caller that forgets to set a
	// mode can never start interrupting anyone.
	var opts StopOptions
	if opts.Mode == StopModeBlock {
		t.Fatal("the zero value must not be block")
	}
}

// --- the notice text -----------------------------------------------------------

func TestTheNoticeNamesWhatArrivedAndFromWhom(t *testing.T) {
	got := BuildStopNotice([]SocketAnswer{peekOfSession(t, "/s", demoRoom(t))})
	want := "Landfall: 2 updates in the room (1 finding from Maya · Codex, 1 message from Jhonny). " +
		"They reach your agent with your next message."
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestTheNoticeIsSingularForOneUpdate(t *testing.T) {
	got := BuildStopNotice([]SocketAnswer{owed("/s", 1, 0, 0, 7, "#7 edge.finding [Maya · Codex] — origin 5xx")})
	want := "Landfall: 1 update in the room (1 finding from Maya · Codex). It reaches your agent with your next message."
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestTheNoticeGroupsBySenderAndKindAndCapsTheList(t *testing.T) {
	got := BuildStopNotice([]SocketAnswer{owed("/s", 6, 0, 0, 6,
		"#1 edge.finding [Ana · Claude Code] — a",
		"#2 edge.finding [Ana · Claude Code] — b",
		"#3 artifact.shared [Ben] — c",
		"#4 claim.staged [Cy · Codex] — d",
		"#5 status.changed — e",
		"#6 severity.changed — f",
	)})
	want := "Landfall: 6 updates in the room (2 findings from Ana · Claude Code, 1 artifact from Ben, " +
		"1 staged claim from Cy · Codex, +2 more). They reach your agent with your next message."
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestAQuarantineIsNamedButNotPromisedToTheNextMessage(t *testing.T) {
	// Only events are handed over by UserPromptSubmit; a quarantine reaches the
	// agent on its next room call. So the notice says what happened and makes no
	// delivery promise it cannot keep.
	got := BuildStopNotice([]SocketAnswer{quarantinePeek(t, "/s", "inc-1", 12)})
	if got != "Landfall: the room quarantined 1 item your agent relied on." {
		t.Fatalf("got %q", got)
	}
}

func TestNothingOwedIsNoNoticeAtAll(t *testing.T) {
	if got := BuildStopNotice(nil); got != "" {
		t.Fatalf("got %q", got)
	}
	if got := BuildStopNotice([]SocketAnswer{owed("/s", 0, 0, 7, 7)}); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestTheNoticeIsAlwaysOneBoundedLine(t *testing.T) {
	name := strings.Repeat("n", 500)
	got := BuildStopNotice([]SocketAnswer{owed("/s", 1, 0, 0, 1, "#1 edge.finding [evil\nname "+name+"] — x")})
	if strings.ContainsAny(got, "\n\r") {
		t.Fatalf("a display name must not break the line: %q", got)
	}
	if n := len([]rune(got)); n > stopNoticeMax {
		t.Fatalf("notice is %d runes", n)
	}
}

func TestParseDigestLineReadsBackWhatFormatEventLineWrote(t *testing.T) {
	seq := int64(7)
	cases := []struct {
		evt      client.Event
		typ, who string
	}{
		{client.Event{Seq: &seq, Type: "edge.finding", Payload: map[string]any{"displayName": "Maya", "edgeAgentLabel": "Codex", "text": "x [y] — z"}}, "edge.finding", "Maya · Codex"},
		{client.Event{Seq: &seq, Type: "chat.message", Payload: map[string]any{"displayName": "Jhonny", "text": "@pick look"}}, "chat.message", "Jhonny"},
		{client.Event{Seq: &seq, Type: "status.changed", Payload: map[string]any{"text": "mitigating"}}, "status.changed", ""},
		{client.Event{Type: "claim.admitted", Payload: map[string]any{"claimSeq": float64(3)}}, "claim.admitted", ""},
	}
	for _, c := range cases {
		line := narrate.FormatEventLine(c.evt)
		// The daemon appends this to addressed chat; it must not disturb the read.
		for _, l := range []string{line, line + "  ← addressed to a person"} {
			typ, who := ParseDigestLine(l)
			if typ != c.typ || who != c.who {
				t.Fatalf("%q → (%q, %q), want (%q, %q)", l, typ, who, c.typ, c.who)
			}
		}
	}
	if typ, who := ParseDigestLine("something else entirely"); typ != "" || who != "" {
		t.Fatalf("an unknown shape must read as an unattributed update, got (%q, %q)", typ, who)
	}
}

// --- the handler, inform ------------------------------------------------------

func TestByDefaultStopNeverBlocksAndConsumesNothing(t *testing.T) {
	s := demoRoom(t)
	var emitted, channel string
	res := RunStopHook(StopOptions{
		Input: `{"hook_event_name":"Stop","stop_hook_active":false}`,
		Query: liveQuery(t, s),
		Send: func(string, SocketRequest) error {
			t.Fatal("inform must not consume: the agent has not seen these yet")
			return nil
		},
		Emit: func(text, ch string) { emitted, channel = text, ch },
	})
	if res.ExitCode != 0 || res.Blocked || !res.Informed {
		t.Fatalf("got %+v", res)
	}
	if channel != ChannelStdout {
		t.Fatalf("channel %q", channel)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(emitted), &body); err != nil {
		t.Fatalf("stdout must be one JSON object, got %q: %v", emitted, err)
	}
	// Exactly one key. `decision` would make the host continue the agent, and
	// Codex rejects any key its schema does not list.
	if len(body) != 1 || body["systemMessage"] != res.Reason {
		t.Fatalf("got %v", body)
	}
	if len(s.Pending()) != 2 {
		t.Fatalf("pending %d, want both updates still owed", len(s.Pending()))
	}
}

func TestTheAnnouncedUpdatesReachTheAgentOnTheNextMessageAndTheNoticeGoesQuiet(t *testing.T) {
	s := demoRoom(t)

	// 1. The agent stops. The person is told; nothing moves.
	var notice string
	RunStopHook(StopOptions{Query: liveQuery(t, s), Send: liveConsume(s), Emit: func(text, _ string) { notice = text }})
	if !strings.Contains(notice, "They reach your agent with your next message.") {
		t.Fatalf("got %q", notice)
	}

	// 2. The person types their next instruction. UserPromptSubmit hands the
	// news to the agent as additionalContext, before the model generates anything.
	var injected string
	out := RunUserPromptSubmitHook(UserPromptSubmitOptions{
		Query: liveQuery(t, s),
		Send:  liveConsume(s),
		Emit:  func(text string) { injected = text },
	})
	if !out.Injected {
		t.Fatalf("got %+v", out)
	}
	var payload PromptOutput
	if err := json.Unmarshal([]byte(injected), &payload); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"#41 edge.finding [Maya · Codex] — origin 5xx began at 14:02", "#42 chat.message [Jhonny]"} {
		if !strings.Contains(payload.HookSpecificOutput.AdditionalContext, want) {
			t.Fatalf("additionalContext is missing %q:\n%s", want, payload.HookSpecificOutput.AdditionalContext)
		}
	}

	// 3. The agent finishes that turn. Nothing new: no notice at all.
	res := RunStopHook(StopOptions{
		Query: liveQuery(t, s),
		Emit:  func(text, _ string) { t.Fatalf("delivered news must not be announced again: %q", text) },
	})
	if res.ExitCode != 0 || res.Informed || res.Blocked {
		t.Fatalf("got %+v", res)
	}
}

func TestUnderCursorInformSaysOnlyTheEmptyObjectAndAsksNothing(t *testing.T) {
	var emitted string
	res := RunStopHook(StopOptions{
		Protocol: CURSOR_JSON,
		Query: func(SocketRequest) ([]SocketAnswer, error) {
			t.Fatal("Cursor can only be told something by starting a turn, so there is nothing to ask")
			return nil, nil
		},
		Emit: func(text, _ string) { emitted = text },
	})
	if res.ExitCode != 0 || res.Blocked || strings.TrimSpace(emitted) != "{}" {
		t.Fatalf("got %+v / %q", res, emitted)
	}
}

func TestInformStaysQuietOnTheLoopGuardAndAnInterruptedTurn(t *testing.T) {
	for _, input := range []string{`{"stop_hook_active":true}`, `{"status":"aborted"}`} {
		res := RunStopHook(StopOptions{
			Input: input,
			Query: liveQuery(t, demoRoom(t)),
			Emit:  func(text, _ string) { t.Fatalf("%s: got %q", input, text) },
		})
		if res.ExitCode != 0 || res.Informed {
			t.Fatalf("%s: got %+v", input, res)
		}
	}
}

func TestTheRegisteredHandlerInformsByDefaultAndBlocksOnlyWhenAsked(t *testing.T) {
	ws := tempWorkspace(t)
	s := demoRoom(t)
	bound := StartHookSocket(context.Background(), s, StartOptions{
		Workspace: ws, PID: 4242,
		Consume: func(sess SocketSession, upTo int64) int64 { return sess.(*session.Session).ConsumeUpTo(upTo) },
	})
	if bound == nil {
		t.Fatal("expected the socket to bind")
	}
	t.Cleanup(func() { _ = bound.Close() })

	var emitted []string
	got := RunHookEvent(context.Background(), "stop", HookDeps{
		Workspace: ws, Host: "codex", Input: "{}",
		Emit: func(text, _ string) { emitted = append(emitted, text) },
	})
	if got.ExitCode != 0 || got.Result != "informed" || len(emitted) != 1 {
		t.Fatalf("got %+v / %v", got, emitted)
	}
	if !strings.HasPrefix(emitted[0], `{"systemMessage":"Landfall: 2 updates in the room`) {
		t.Fatalf("got %q", emitted[0])
	}
	if len(s.Pending()) != 2 {
		t.Fatalf("pending %d", len(s.Pending()))
	}

	emitted = nil
	got = RunHookEvent(context.Background(), "stop", HookDeps{
		Workspace: ws, Host: "codex", Input: "{}", StopMode: StopModeBlock,
		Emit: func(text, _ string) { emitted = append(emitted, text) },
	})
	if got.ExitCode != 2 || got.Result != "blocked" || len(emitted) != 1 || !strings.Contains(emitted[0], "Do not conclude yet") {
		t.Fatalf("got %+v / %v", got, emitted)
	}
	if len(s.Pending()) != 0 {
		t.Fatalf("block mode consumes what it reported, pending %d", len(s.Pending()))
	}
}
