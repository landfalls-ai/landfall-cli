package cli

// harness_serve_test.go — two agent hosts on one machine, one room daemon, one
// share link: the case that used to collapse into ONE agent session labelled
// "edge-agent" (2026-09-28). Real `serve` front ends over pipes, a real daemon
// on a real socket, fake edge clients on the daemon's side.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/daemon"
	"github.com/landfalls-ai/landfall-cli/internal/hooks"
	"github.com/landfalls-ai/landfall-cli/internal/session"
)

// labelledEdge is the daemon's client for one seat: its instance id is derived
// from the label it joined under, so a test can tell seats apart.
type labelledEdge struct {
	*fakeEdge
	id string
}

func (l *labelledEdge) Join(ctx context.Context) (*client.JoinResult, error) {
	if _, err := l.fakeEdge.Join(ctx); err != nil {
		return nil, err
	}
	return &client.JoinResult{AgentInstanceID: l.id}, nil
}
func (l *labelledEdge) AgentInstanceID() string { return l.id }
func (l *labelledEdge) GetContextFrame(context.Context) (*client.ContextFrame, error) {
	return &client.ContextFrame{}, nil
}

type daemonJoins struct {
	mu     sync.Mutex
	labels []string
	tokens []string
}

func (j *daemonJoins) snapshot() ([]string, []string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]string(nil), j.labels...), append([]string(nil), j.tokens...)
}

func startTestDaemon(t *testing.T, ws hooks.Workspace) *daemonJoins {
	t.Helper()
	joins := &daemonJoins{}
	d := daemon.New(daemon.Options{
		Workspace: ws,
		IdleGrace: time.Hour,
		Deps: daemon.Deps{
			NewClient: func(cfg client.Config) session.EdgeClient {
				joins.mu.Lock()
				joins.labels = append(joins.labels, cfg.AgentLabel)
				joins.tokens = append(joins.tokens, cfg.Token)
				joins.mu.Unlock()
				return &labelledEdge{fakeEdge: &fakeEdge{cfg: cfg}, id: "inst-" + strings.ReplaceAll(strings.ToLower(cfg.AgentLabel), " ", "-")}
			},
			Heartbeat: time.Hour,
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = d.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(3 * time.Second)
	for !daemon.Reachable(ws) {
		if time.Now().After(deadline) {
			t.Fatal("the test daemon never answered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return joins
}

type frontEndRun struct {
	stdin  *io.PipeWriter
	out    *bufio.Reader
	errBuf *lockedBuffer
	done   chan error
}

func startFrontEnd(t *testing.T, ws hooks.Workspace, link string, resolve func(context.Context, *UI, string) (*client.Config, error)) *frontEndRun {
	t.Helper()
	r := launchFrontEnd(t, ws, link, resolve)
	r.waitFor(t, "MCP stdio server ready")
	return r
}

func launchFrontEnd(t *testing.T, ws hooks.Workspace, link string, resolve func(context.Context, *UI, string) (*client.Config, error)) *frontEndRun {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	r := &frontEndRun{stdin: inW, out: bufio.NewReader(outR), errBuf: &lockedBuffer{}, done: make(chan error, 1)}
	ui := &UI{Out: &bytes.Buffer{}, Err: r.errBuf}
	so := serveOptions{
		In: inR, Out: outW, Workspace: ws,
		Resolve:      resolve,
		EnsureDaemon: func(hooks.Workspace, func(string)) bool { return true },
		Redeem: func(context.Context, string) (client.Config, error) {
			return client.Config{}, errors.New("could not join the war room (HTTP 410): the link expired or was already used")
		},
	}
	go func() {
		err := runServe(context.Background(), ui, link, so)
		_ = outW.Close()
		r.done <- err
	}()
	t.Cleanup(func() {
		_ = inW.Close()
		select {
		case <-r.done:
		case <-time.After(5 * time.Second):
		}
	})
	return r
}

func (r *frontEndRun) waitFor(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(r.errBuf.String(), want) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q; stderr:\n%s\n%s", want, r.errBuf.String(), goroutineDump())
}

func (r *frontEndRun) initialize(t *testing.T, clientName string) {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"` + clientName + `","version":"1"}}}`
	if _, err := io.WriteString(r.stdin, body+"\n"); err != nil {
		t.Fatal(err)
	}
	line, err := r.out.ReadBytes('\n')
	if err != nil {
		t.Fatalf("no initialize answer: %v", err)
	}
	var res map[string]any
	if json.Unmarshal(line, &res) != nil || res["result"] == nil {
		t.Fatalf("initialize answered %s", line)
	}
}

func TestTwoHostsOnOneMachineJoinAsTwoAgentSessionsWithOneLink(t *testing.T) {
	t.Setenv("LANDFALL_AGENT_LABEL", "")
	dir, runDir := shortTempDir(t), shortTempDir(t)
	ws := hooks.Workspace{Cwd: dir, Env: map[string]string{"XDG_RUNTIME_DIR": runDir}}
	joins := startTestDaemon(t, ws)

	const link = "https://app.landfalls.test/j/abc123"
	// The closed port makes every direct read the front end's own tools might
	// make fail at once instead of reaching anything.
	cfg := &client.Config{BaseURL: "http://127.0.0.1:1", Slug: "acme", IncidentID: "inc-1", Token: "edge-token", AgentLabel: defaultAgentLabel}
	redeemed := 0
	firstResolve := func(context.Context, *UI, string) (*client.Config, error) {
		redeemed++
		c := *cfg
		return &c, nil
	}
	// The link is single-use: a second redeem is refused, as the server does.
	secondResolve := func(context.Context, *UI, string) (*client.Config, error) {
		return nil, errors.New("could not join the war room (HTTP 410): the link expired or was already used")
	}

	claude := startFrontEnd(t, ws, link, firstResolve)
	if labels, _ := joins.snapshot(); len(labels) != 0 {
		t.Fatalf("in daemon mode the join waits for initialize, so it can carry the host's name; joined %v", labels)
	}
	claude.initialize(t, "claude-code")
	claude.waitFor(t, `joined incident inc-1 as "Claude Code" (instance inst-claude-code)`)

	codex := startFrontEnd(t, ws, link, secondResolve)
	codex.initialize(t, "codex-mcp-client")
	codex.waitFor(t, `joined incident inc-1 as "Codex" (instance inst-codex)`)
	codex.waitFor(t, "this link was already redeemed on this machine (incident inc-1)")

	labels, tokens := joins.snapshot()
	if strings.Join(labels, ",") != "Claude Code,Codex" {
		t.Fatalf("want one /edge/join per host under its own label, got %v", labels)
	}
	if tokens[1] != "edge-token" {
		t.Fatalf("the second host joins with the session the link already gave this machine, got %q", tokens[1])
	}
	if redeemed != 1 {
		t.Fatalf("the link must be redeemed once, got %d", redeemed)
	}

	res, err := daemon.Send(hooks.DaemonSocketPath(ws), daemon.Request{Op: "rooms"}, time.Second)
	if err != nil || len(res.Rooms) != 1 || len(res.Rooms[0].Seats) != 2 {
		t.Fatalf("rooms: %v %+v", err, res)
	}
	terminals := map[string]bool{}
	for _, rd := range res.Rooms[0].Readers {
		if rd.Kind == daemon.KindTerminal {
			terminals[rd.Name] = true
		}
	}
	key := hooks.WorkspaceKey(dir)
	if !terminals[hooks.TerminalReaderNameFor(key, "claude-code")] || !terminals[hooks.TerminalReaderNameFor(key, "codex")] {
		t.Fatalf("the person needs one reader per host in this checkout: %v", terminals)
	}
}

func (r *frontEndRun) call(t *testing.T, id int, tool string) map[string]any {
	t.Helper()
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q,"arguments":{}}}`, id, tool)
	if _, err := io.WriteString(r.stdin, body+"\n"); err != nil {
		t.Fatal(err)
	}
	line, err := r.out.ReadBytes('\n')
	if err != nil {
		t.Fatalf("no answer to %s: %v", tool, err)
	}
	var res map[string]any
	if json.Unmarshal(line, &res) != nil {
		t.Fatalf("%s answered %s", tool, line)
	}
	return res
}

// Two hosts started together with the same --link: the link is redeemed once,
// the second waits for the first redeem rather than spending the link again.
func TestTwoHostsStartedTogetherRedeemTheLinkOnce(t *testing.T) {
	t.Setenv("LANDFALL_AGENT_LABEL", "")
	dir, runDir := shortTempDir(t), shortTempDir(t)
	ws := hooks.Workspace{Cwd: dir, Env: map[string]string{"XDG_RUNTIME_DIR": runDir}}
	joins := startTestDaemon(t, ws)

	const link = "https://app.landfalls.test/j/together"
	var mu sync.Mutex
	redeemed := 0
	resolve := func(context.Context, *UI, string) (*client.Config, error) {
		mu.Lock()
		redeemed++
		n := redeemed
		mu.Unlock()
		if n > 1 {
			return nil, errors.New("could not join the war room (HTTP 410): the link expired or was already used")
		}
		time.Sleep(300 * time.Millisecond) // a slow redeem, so the sibling has to wait for it
		return &client.Config{BaseURL: "http://127.0.0.1:1", Slug: "acme", IncidentID: "inc-1", Token: "edge-token"}, nil
	}
	a := launchFrontEnd(t, ws, link, resolve)
	b := launchFrontEnd(t, ws, link, resolve)
	a.waitFor(t, "MCP stdio server ready")
	b.waitFor(t, "MCP stdio server ready")
	a.initialize(t, "claude-code")
	b.initialize(t, "codex-mcp-client")
	a.waitFor(t, "joined incident inc-1")
	b.waitFor(t, "joined incident inc-1")
	mu.Lock()
	defer mu.Unlock()
	if redeemed != 1 {
		t.Fatalf("the link must be redeemed once, got %d", redeemed)
	}
	if labels, _ := joins.snapshot(); len(labels) != 2 {
		t.Fatalf("two sessions: %v", labels)
	}
}

// initialize is answered at once even while the join is slow; a tool call
// waits for the join to land.
func TestInitializeIsAnsweredBeforeASlowJoinAndToolsWaitForIt(t *testing.T) {
	t.Setenv("LANDFALL_AGENT_LABEL", "")
	dir, runDir := shortTempDir(t), shortTempDir(t)
	ws := hooks.Workspace{Cwd: dir, Env: map[string]string{"XDG_RUNTIME_DIR": runDir}}
	startSlowDaemon(t, ws, 700*time.Millisecond)
	cfg := &client.Config{BaseURL: "http://127.0.0.1:1", Slug: "acme", IncidentID: "inc-1", Token: "t"}
	fe := startFrontEnd(t, ws, "", func(context.Context, *UI, string) (*client.Config, error) { c := *cfg; return &c, nil })
	began := time.Now()
	fe.initialize(t, "codex-mcp-client")
	if took := time.Since(began); took > 500*time.Millisecond {
		t.Fatalf("initialize waited %s for the join; a host gives a server only seconds", took)
	}
	res := fe.call(t, 2, "describe_widget_types")
	if res["result"] == nil {
		t.Fatalf("a tool call after initialize waits for the join and runs: %v", res)
	}
	fe.waitFor(t, `joined incident inc-1 as "Codex"`)
}

func startSlowDaemon(t *testing.T, ws hooks.Workspace, delay time.Duration) {
	t.Helper()
	d := daemon.New(daemon.Options{
		Workspace: ws,
		IdleGrace: time.Hour,
		Deps: daemon.Deps{
			NewClient: func(cfg client.Config) session.EdgeClient {
				return &slowEdge{labelledEdge: labelledEdge{fakeEdge: &fakeEdge{cfg: cfg}, id: "inst-slow"}, delay: delay}
			},
			Heartbeat: time.Hour,
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = d.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(3 * time.Second)
	for !daemon.Reachable(ws) {
		if time.Now().After(deadline) {
			t.Fatal("the test daemon never answered")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type slowEdge struct {
	labelledEdge
	delay time.Duration
}

func (s *slowEdge) Join(ctx context.Context) (*client.JoinResult, error) {
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.labelledEdge.Join(ctx)
}
