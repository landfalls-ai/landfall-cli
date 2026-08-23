package hooks

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// --- spec.go (T038) ---------------------------------------------------------

func TestHookEventsAreTheFourEventsInRegistrationOrder(t *testing.T) {
	want := []string{"stop", "file-changed", "user-prompt-submit", "pre-tool-use"}
	got := HookEventIDs()
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestEventsForHost(t *testing.T) {
	// file-changed is Claude Code only: it is the one host with a file-watch
	// hook. pre-tool-use is not registered for Cursor — an entry nothing yet
	// honors is worse than no entry.
	cases := map[string][]string{
		"claude-code": {"stop", "file-changed", "user-prompt-submit", "pre-tool-use"},
		"codex":       {"stop", "pre-tool-use"},
		"cursor":      {"stop"},
		"unknown":     {},
	}
	for host, want := range cases {
		got := EventsForHost(host)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%s: got %v, want %v", host, got, want)
		}
	}
}

func TestHookCommandAppendsTheHostFlagOnlyWhereItChangesBehaviour(t *testing.T) {
	// The two hosts already registered with the bare form must keep
	// byte-identical entries, so an upgrade churns nobody's config into a
	// conflict.
	if got := HookCommand("stop", "claude-code"); got != "landfall hooks stop" {
		t.Fatalf("got %q", got)
	}
	if got := HookCommand("pre-tool-use", "codex"); got != "landfall hooks pre-tool-use" {
		t.Fatalf("got %q", got)
	}
	if got := HookCommand("stop", "cursor"); got != "landfall hooks stop --host cursor" {
		t.Fatalf("got %q", got)
	}
	// An unknown host falls back to the convention two of the three share.
	if got := HookCommand("stop", "brand-new-ide"); got != "landfall hooks stop" {
		t.Fatalf("got %q", got)
	}
}

func TestMatcherFor(t *testing.T) {
	if got := MatcherFor("file-changed"); got != "room_events" {
		t.Fatalf("got %q", got)
	}
	// PreToolUse's matcher is load-bearing: scoping to the shell tool means an
	// Edit, a Read or a web fetch costs nothing, not even a process.
	if got := MatcherFor("pre-tool-use"); got != "Bash" {
		t.Fatalf("got %q", got)
	}
	// UserPromptSubmit does not support matchers and always fires.
	if got := MatcherFor("user-prompt-submit"); got != "" {
		t.Fatalf("got %q", got)
	}
	if got := MatcherFor("stop"); got != "" {
		t.Fatalf("got %q", got)
	}
	if got := MatcherFor("nope"); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestSupersededHookCommandsEnumeratesCursorsBareV020Form(t *testing.T) {
	// The mechanism is what makes changing a registered command survivable:
	// without it, a changed command turns every existing install into a conflict
	// that is never overwritten and never removable.
	got := SupersededHookCommands("stop", "cursor")
	if len(got) != 1 || got[0] != "landfall hooks stop" {
		t.Fatalf("got %v", got)
	}
	// And the superseded form is exactly the CURRENT form for the hosts that
	// never changed — so nothing else may claim it.
	for _, pair := range [][2]string{{"stop", "claude-code"}, {"stop", "codex"}, {"pre-tool-use", "claude-code"}} {
		if got := SupersededHookCommands(pair[0], pair[1]); len(got) != 0 {
			t.Fatalf("%v: got %v, want none", pair, got)
		}
	}
}

func TestIsLandfallCommandRecognizesAHandEditedEntry(t *testing.T) {
	for _, c := range []string{
		"landfall hooks stop",
		"  landfall hooks stop --host cursor  ",
		"landfall hooks stop --host cursor --my-own-flag",
	} {
		if !IsLandfallCommand(c) {
			t.Fatalf("%q must be recognized as ours", c)
		}
	}
	for _, c := range []string{"", "npx landfall hooks stop", "somebody-elses-hook", "landfall serve"} {
		if IsLandfallCommand(c) {
			t.Fatalf("%q must be invisible to us", c)
		}
	}
}

// --- run.go (T039) ----------------------------------------------------------

func TestRunHookEventRefusesAnUnknownEventID(t *testing.T) {
	got := RunHookEvent(context.Background(), "not-an-event", HookDeps{})
	if got.ExitCode != 2 || got.Error != "unknown hook event: not-an-event" {
		t.Fatalf("got %+v", got)
	}
}

func TestRunHookEventAnswersTheProtocolCorrectNoOpUntilAHandlerRegisters(t *testing.T) {
	// The installer registers a command the host runs on every matching event
	// from the moment it is written, so silence must be a well-formed answer.
	got := RunHookEvent(context.Background(), "user-prompt-submit", HookDeps{Host: "claude-code"})
	if got.ExitCode != 0 || got.Stdout != "" {
		t.Fatalf("exit2 host: got %+v", got)
	}
	// Cursor JSON.parse()s stdout unconditionally, so an empty stdout is a parse
	// error, not a silent allow.
	got = RunHookEvent(context.Background(), "stop", HookDeps{Host: "cursor"})
	if got.ExitCode != 0 || got.Stdout != "{}\n" {
		t.Fatalf("cursor-json host: got %+v", got)
	}
}

func TestRegisteredHandlersAreDispatchedTo(t *testing.T) {
	// The four real handlers are a separate track (T040-T044); this proves the
	// seam they plug into.
	t.Cleanup(func() {
		handlersMu.Lock()
		delete(handlers, "stop")
		handlersMu.Unlock()
	})
	var saw HookDeps
	RegisterHookHandler("stop", func(_ context.Context, deps HookDeps) HookResult {
		saw = deps
		return HookResult{ExitCode: 2, Stderr: "you still owe the room a look", Result: "blocked"}
	})

	got := RunHookEvent(context.Background(), "stop", HookDeps{Input: `{"stop_hook_active":true}`, Host: "codex"})
	if got.ExitCode != 2 || got.Result != "blocked" {
		t.Fatalf("got %+v", got)
	}
	if saw.Input != `{"stop_hook_active":true}` || saw.Host != "codex" {
		t.Fatalf("the handler must receive the shared stdin read and the host id: %+v", saw)
	}
}

func TestRegisterHookHandlerRefusesAnUnknownEventID(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("a handler registered against a typo'd event would simply never run")
		}
	}()
	RegisterHookHandler("stopp", func(context.Context, HookDeps) HookResult { return HookResult{} })
}

func TestParseEventPayloadNeverFailsOnAMalformedOrAbsentPayload(t *testing.T) {
	if got := ParseEventPayload(""); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
	if got := ParseEventPayload("{not json"); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
	if got := ParseEventPayload("null"); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
	if got := ParseEventPayload(`{"stop_hook_active":true}`); got["stop_hook_active"] != true {
		t.Fatalf("got %v", got)
	}
}

func TestShellCommandOfReadsBothKeySpellings(t *testing.T) {
	snake := ParseEventPayload(`{"tool_name":"Bash","tool_input":{"command":"kubectl get pods"}}`)
	if got := ShellCommandOf(snake); got != "kubectl get pods" {
		t.Fatalf("got %q", got)
	}
	camel := ParseEventPayload(`{"toolName":"shell","toolInput":{"cmd":"aws s3 ls"}}`)
	if got := ShellCommandOf(camel); got != "aws s3 ls" {
		t.Fatalf("got %q", got)
	}
}

func TestShellCommandOfIgnoresEverythingThatIsNotAShellTool(t *testing.T) {
	// Anything that is not a shell tool returns "" and nothing further happens:
	// no policy read, no terminal, no token, no network.
	for _, payload := range []string{
		`{"tool_name":"Edit","tool_input":{"command":"rm -rf /"}}`,
		`{"tool_name":"Bash"}`,
		`{"tool_name":"Bash","tool_input":{"command":"   "}}`,
		`{}`,
	} {
		if got := ShellCommandOf(ParseEventPayload(payload)); got != "" {
			t.Fatalf("%s → %q", payload, got)
		}
	}
}

// --- input.go (T035) --------------------------------------------------------

func TestReadHookInputReadsTheWholePayload(t *testing.T) {
	if got := ReadHookInput(strings.NewReader(`{"a":1}`), 0); got != `{"a":1}` {
		t.Fatalf("got %q", got)
	}
}

func TestReadHookInputAnswersEmptyForNoStream(t *testing.T) {
	if got := ReadHookInput(nil, 0); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestReadHookInputAnswersEmptyForATTY(t *testing.T) {
	// Run by hand at a prompt: there is no payload coming, and a hook that
	// blocks waiting for one hangs the engineer's shell.
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		t.Skip("no controlling terminal in this environment")
	}
	defer func() { _ = f.Close() }()
	if got := ReadHookInput(f, 50*time.Millisecond); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestReadHookInputGivesUpAtTheDeadlineWithWhatItHas(t *testing.T) {
	// A host that writes a partial payload and then stalls must not hang the
	// hook — and what it did send is still worth having.
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte(`{"partial":`))
		// deliberately never closed
	}()
	started := time.Now()
	got := ReadHookInput(pr, 100*time.Millisecond)
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("blocked for %v — the deadline is not being honoured", elapsed)
	}
	if got != `{"partial":` {
		t.Fatalf("got %q", got)
	}
}

func TestReadHookInputStopsAtTheOneMegabyteCap(t *testing.T) {
	// A hook payload is not a stream.
	huge := strings.NewReader(strings.Repeat("x", 3*stdinMax))
	got := ReadHookInput(huge, time.Second)
	if len(got) > stdinMax+64*1024 {
		t.Fatalf("read %d bytes, want the cap to have stopped it near %d", len(got), stdinMax)
	}
	if len(got) < stdinMax {
		t.Fatalf("read only %d bytes, want at least the cap", len(got))
	}
}

// --- confirm.go (T037) ------------------------------------------------------

func TestConfirmDeclinesWithNoControllingTerminal(t *testing.T) {
	// No controlling terminal (CI, a headless daemon, a detached process group)
	// is the clearest possible "no" — the acceptance criterion is "nothing
	// leaves without the confirm".
	if ConfirmOnTTY("send it?", ConfirmOptions{TTYPath: "/no/such/tty"}) {
		t.Fatal("must decline")
	}
}

func TestConfirmDeclinesWhenTheFdIsNotATerminal(t *testing.T) {
	// A redirected /dev/tty in a test or CI harness.
	p := t.TempDir() + "/not-a-tty"
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if ConfirmOnTTY("send it?", ConfirmOptions{TTYPath: p}) {
		t.Fatal("must decline")
	}
}

func TestConfirmAcceptsOnlyY(t *testing.T) {
	// The default answer to the prompt is no: `y` confirms, and every other key,
	// including Enter, declines.
	for _, key := range []string{"y", "Y", " y", "y\n"} {
		if !isYes(key) {
			t.Fatalf("%q must confirm", key)
		}
	}
	for _, key := range []string{"n", "N", "\n", "\r", "", " ", "yes\n"[1:], "\x03"} {
		if isYes(key) {
			t.Fatalf("%q must decline", key)
		}
	}
}
