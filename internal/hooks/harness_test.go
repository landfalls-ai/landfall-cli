package hooks

import (
	"context"
	"testing"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

func TestHarnessFromClientNameMapsTheKnownHosts(t *testing.T) {
	cases := map[string]Harness{
		"claude-code":                   {Key: "claude-code", Label: "Claude Code"},
		"codex-mcp-client":              {Key: "codex", Label: "Codex"},
		"codex":                         {Key: "codex", Label: "Codex"},
		"cursor":                        {Key: "cursor", Label: "Cursor"},
		"cursor-vscode":                 {Key: "cursor", Label: "Cursor"},
		"Visual Studio Code":            {Key: "vscode", Label: "VS Code"},
		"Visual Studio Code - Insiders": {Key: "vscode", Label: "VS Code"},
		"vscode":                        {Key: "vscode", Label: "VS Code"},
		"windsurf":                      {Key: "windsurf", Label: "Windsurf"},
		"Zed Agent":                     {Key: "zed-agent", Label: "Zed Agent"},
		"  my<script>host\x00  ":        {Key: "my-script-host", Label: "myscripthost"},
		"":                              {},
		"<>!!":                          {},
	}
	for name, want := range cases {
		if got := HarnessFromClientName(name); got != want {
			t.Errorf("HarnessFromClientName(%q) = %+v, want %+v", name, got, want)
		}
	}
}

func TestDetectHookHarnessReadsTheFlagThenTheHostsOwnMarkers(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }
	cases := []struct {
		name  string
		flag  string
		input string
		env   map[string]string
		want  string
	}{
		{"flag wins", "cursor", "", map[string]string{"CLAUDECODE": "1"}, "cursor"},
		{"explicit override", "", "", map[string]string{"LANDFALL_HARNESS": "codex", "CLAUDECODE": "1"}, "codex"},
		{"claude code env", "", "", map[string]string{"CLAUDECODE": "1"}, "claude-code"},
		{"codex transcript", "", `{"transcript_path":"/Users/p/.codex/sessions/2026/09/28/rollout.jsonl"}`, nil, "codex"},
		{"claude transcript", "", `{"transcript_path":"/Users/p/.claude/projects/x/abc.jsonl"}`, nil, "claude-code"},
		{"codex env", "", `{}`, map[string]string{"CODEX_THREAD_ID": "t-1"}, "codex"},
		{"nothing says", "", `not json`, nil, ""},
	}
	for _, c := range cases {
		if got := DetectHookHarness(c.flag, c.input, env(c.env)); got != c.want {
			t.Errorf("%s: DetectHookHarness = %q, want %q", c.name, got, c.want)
		}
	}
	if got := DetectHookHarness("", "", nil); got != "" {
		t.Errorf("a nil getenv must read as nothing, got %q", got)
	}
}

func TestTerminalReaderNameForKeepsTheHarnessLessNameForUnknown(t *testing.T) {
	if TerminalReaderNameFor("ws", "") != TerminalReaderName("ws") {
		t.Fatal("no harness is the harness-less reader every earlier build used")
	}
	if TerminalReaderNameFor("ws", "codex") != "terminal:ws:codex" {
		t.Fatalf("got %q", TerminalReaderNameFor("ws", "codex"))
	}
}

// Two harnesses in one checkout, each with its own serve: a Codex hook asking
// every per-pid socket must neither read nor consume Claude Code's session.
func TestAPerPidSocketAnswersOnlyItsOwnHarnessesHooks(t *testing.T) {
	ws := tempWorkspace(t)
	n := int64(7)
	s := &fakeSession{pending: []client.Event{{Seq: &n, Type: "edge.finding", Payload: map[string]any{"text": "p99 spiked"}}}, cursor: 6, cfg: &client.Config{IncidentID: "inc-1"}}
	consumed := int64(-1)
	sock := StartHookSocket(context.Background(), s, StartOptions{
		Workspace: ws, PID: 4242,
		Consume: func(_ SocketSession, upTo int64) int64 { consumed = upTo; return upTo },
		Harness: func() string { return "claude-code" },
	})
	if sock == nil {
		t.Fatal("socket did not bind")
	}
	defer func() { _ = sock.Close() }()

	codex := ws
	codex.Harness = "codex"
	answers := QueryHookSockets(PeekRequest(), codex, 0)
	if len(answers) != 1 || answers[0].Response.CountOr(0) != 0 || answers[0].Response.IncidentID != "" {
		t.Fatalf("a Codex hook must be answered as if there were no session here: %+v", answers)
	}
	if err := SendToAnswer(codex, sock.SocketPath, ConsumeRequest(7), 0); err != nil {
		t.Fatal(err)
	}
	if consumed != -1 {
		t.Fatal("a Codex hook consumed Claude Code's queue")
	}

	claude := ws
	claude.Harness = "claude-code"
	if a := QueryHookSockets(PeekRequest(), claude, 0); len(a) != 1 || a[0].Response.CountOr(0) != 1 {
		t.Fatalf("Claude Code's own hook reads its session: %+v", a)
	}
	// A hook that cannot tell which host ran it is answered, as always.
	if a := QueryHookSockets(PeekRequest(), ws, 0); len(a) != 1 || a[0].Response.CountOr(0) != 1 {
		t.Fatalf("an unidentified hook is answered: %+v", a)
	}
}
