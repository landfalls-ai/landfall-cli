package cli

// serve_test.go — `landfall serve` end to end, over real pipes, against a fake
// edge client. Nothing here mocks the MCP server, the session, the tool surface
// or the hook socket: the point is to prove the WIRING, which is the only thing
// serve.go itself contributes. Each piece already has its own unit tests in its
// own package, and all of them passing while serve hands the wrong session to
// the wrong server is exactly the failure this file exists to catch.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/hooks"
	"github.com/landfalls-ai/landfall-cli/internal/mcp"
	"github.com/landfalls-ai/landfall-cli/internal/session"
)

// --- a fake edge client -----------------------------------------------------

// fakeEdge is a session.EdgeClient that touches no network. It records the
// three calls serve's own lifecycle makes (join, heartbeat, leave); everything
// else answers emptily, because the tools' own behavior is tools' test's
// business, not this file's.
type fakeEdge struct {
	cfg client.Config

	mu        sync.Mutex
	joined    int
	left      int
	heartbeat int
	joinErr   error
}

func (f *fakeEdge) Join(context.Context) (*client.JoinResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.joinErr != nil {
		return nil, f.joinErr
	}
	f.joined++
	return &client.JoinResult{AgentInstanceID: "inst-1"}, nil
}

func (f *fakeEdge) Leave(context.Context) error {
	f.mu.Lock()
	f.left++
	f.mu.Unlock()
	return nil
}

func (f *fakeEdge) Heartbeat(context.Context, string) error {
	f.mu.Lock()
	f.heartbeat++
	f.mu.Unlock()
	return nil
}

func (f *fakeEdge) counts() (joined, left, heartbeat int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.joined, f.left, f.heartbeat
}

func (f *fakeEdge) Contribute(context.Context, string, map[string]any) error { return nil }
func (f *fakeEdge) UploadArtifact(context.Context, string, string, string) (*client.ArtifactResult, error) {
	return &client.ArtifactResult{}, nil
}
func (f *fakeEdge) FlagContext(context.Context, int64, string, string) error   { return nil }
func (f *fakeEdge) PositionClaim(context.Context, int64, string, string) error { return nil }
func (f *fakeEdge) StageClaim(context.Context, map[string]any) error           { return nil }
func (f *fakeEdge) GetBrief(context.Context) ([]client.Event, error)           { return nil, nil }
func (f *fakeEdge) GetUpdates(context.Context, int64) ([]client.Event, error)  { return nil, nil }
func (f *fakeEdge) GetContextFrame(context.Context) (*client.ContextFrame, error) {
	return nil, errors.New("no frame")
}
func (f *fakeEdge) GetContextDelta(context.Context, int64) (*client.FrameDelta, error) {
	return nil, nil
}
func (f *fakeEdge) SearchContext(context.Context, string) (*client.SearchResult, error) {
	return nil, nil
}
func (f *fakeEdge) GetAttention(context.Context) (*client.Attention, error)   { return nil, nil }
func (f *fakeEdge) GetDivergence(context.Context) (*client.Divergence, error) { return nil, nil }
func (f *fakeEdge) AgentInstanceID() string                                   { return "inst-1" }
func (f *fakeEdge) Config() client.Config                                     { return f.cfg }

var _ session.EdgeClient = (*fakeEdge)(nil)

// --- a harness --------------------------------------------------------------

// serveHarness runs one `serve` against pipes, in its own workspace, with the
// hook socket and doorbell both redirected into a temp dir so a test never
// touches the developer's real runtime directory or repository.
type serveHarness struct {
	t *testing.T

	stdin  *io.PipeWriter
	out    *bufio.Reader
	errBuf *lockedBuffer

	edge *fakeEdge
	ws   hooks.Workspace
	dir  string

	// ui is serve's own UI. The test keeps it to assert the stdout discipline:
	// ui.Out must be io.Discard for the whole command, because stdout is the
	// MCP wire and an incidental ui.Outf there would corrupt a stream the agent
	// is parsing.
	ui *UI

	done chan error
}

// lockedBuffer is stderr for the harness: serve writes to it from the command
// goroutine while the test reads it from its own.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type harnessOptions struct {
	// cfg is what Resolve answers. Nil means "not configured" — the un-joined
	// path.
	cfg     *client.Config
	version string
	ctx     context.Context
}

// shortTempDir is t.TempDir() with a short path. Deliberately under /tmp: on
// macOS $TMPDIR is /var/folders/<…>/T/<test name>/<n>/, and
// `<base>/landfall/<16-char key>/<pid>.sock` puts a real socket path well over
// the 104-byte sockaddr_un.sun_path limit — the same reason
// internal/hooks/socket_test.go's own tempWorkspace does this.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "lf-serve-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func startServe(t *testing.T, opts harnessOptions) *serveHarness {
	t.Helper()

	dir, runDir := shortTempDir(t), shortTempDir(t)

	h := &serveHarness{
		t:      t,
		errBuf: &lockedBuffer{},
		edge:   &fakeEdge{},
		dir:    dir,
		ws:     hooks.Workspace{Cwd: dir, Env: map[string]string{"XDG_RUNTIME_DIR": runDir}},
		done:   make(chan error, 1),
	}
	if opts.cfg != nil {
		h.edge.cfg = *opts.cfg
	}

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	h.stdin = inW
	h.out = bufio.NewReader(outR)

	// Deliberately NOT io.Discard here: serve is the thing under test that has
	// to set that itself, and a buffer is how the test can tell whether it did.
	ui := &UI{Out: &bytes.Buffer{}, Err: h.errBuf}
	h.ui = ui

	ctx := opts.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	so := serveOptions{
		In:        inR,
		Out:       outW,
		Workspace: h.ws,
		Version:   opts.version,
		Resolve: func(context.Context, *UI, string) (*client.Config, error) {
			return opts.cfg, nil
		},
		NewClient: func(cfg client.Config) session.EdgeClient {
			h.edge.cfg = cfg
			return h.edge
		},
		// A tiny beat so the joined test can prove presence actually started
		// without sleeping for the real 15 seconds.
		HeartbeatInterval: 5 * time.Millisecond,
	}

	go func() {
		err := runServe(ctx, ui, "", so)
		_ = outW.Close()
		h.done <- err
	}()

	// The ready line is the sequencing signal: everything serve sets up
	// (doorbell, join, socket) has happened by the time it is written.
	h.waitForStderr("MCP stdio server ready")
	return h
}

func (h *serveHarness) waitForStderr(want string) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(h.errBuf.String(), want) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for %q on stderr; got:\n%s", want, h.errBuf.String())
}

// rpc writes one JSON-RPC frame and reads the one response it produces.
func (h *serveHarness) rpc(body string) map[string]any {
	h.t.Helper()
	if _, err := io.WriteString(h.stdin, body+"\n"); err != nil {
		h.t.Fatalf("write to serve stdin: %v", err)
	}
	line, err := h.out.ReadBytes('\n')
	if err != nil {
		h.t.Fatalf("read from serve stdout: %v (stderr:\n%s)", err, h.errBuf.String())
	}
	var res map[string]any
	if err := json.Unmarshal(line, &res); err != nil {
		h.t.Fatalf("serve wrote a non-JSON frame %q: %v", line, err)
	}
	return res
}

// closeStdin ends the MCP loop the way a departing agent does, and waits for
// serve to return.
func (h *serveHarness) closeStdin() error {
	h.t.Helper()
	_ = h.stdin.Close()
	select {
	case err := <-h.done:
		return err
	case <-time.After(5 * time.Second):
		h.t.Fatal("serve did not return after stdin closed")
		return nil
	}
}

func (h *serveHarness) waitDone() error {
	h.t.Helper()
	select {
	case err := <-h.done:
		return err
	case <-time.After(5 * time.Second):
		h.t.Fatalf("serve did not return; stderr:\n%s", h.errBuf.String())
		return nil
	}
}

func result(t *testing.T, res map[string]any) map[string]any {
	t.Helper()
	if e, ok := res["error"]; ok {
		t.Fatalf("JSON-RPC error where a result was expected: %v", e)
	}
	r, ok := res["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result object in %v", res)
	}
	return r
}

// --- the wiring test --------------------------------------------------------

// TestServeInitializeAndToolsList is the end-to-end wiring assertion: a real
// JSON-RPC `initialize` + `tools/list` over a pipe standing in for stdio, with
// the whole command running exactly as it does in production apart from the
// client at the far end.
func TestServeInitializeAndToolsList(t *testing.T) {
	cfg := &client.Config{
		BaseURL:    "https://landfall.test",
		Slug:       "acme",
		IncidentID: "inc-1",
		Token:      "t",
		AgentLabel: "edge-agent",
	}
	h := startServe(t, harnessOptions{cfg: cfg, version: "1.2.3-test"})

	// 1. initialize — the ServerInfo version must be the injected build version,
	//    NOT bin/landfall.mjs:487's hardcoded '0.2.0'.
	init := result(t, h.rpc(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	if got := init["protocolVersion"]; got != mcp.ProtocolVersion {
		t.Errorf("protocolVersion = %v, want %v", got, mcp.ProtocolVersion)
	}
	info, ok := init["serverInfo"].(map[string]any)
	if !ok {
		t.Fatalf("no serverInfo in %v", init)
	}
	if info["name"] != "landfall" {
		t.Errorf("serverInfo.name = %v, want landfall", info["name"])
	}
	if info["version"] != "1.2.3-test" {
		t.Errorf("serverInfo.version = %v, want the injected build version", info["version"])
	}
	// The standing edge-investigator guidance has to reach the client on
	// connect, or an agent joins with no operating instructions at all.
	instructions, _ := init["instructions"].(string)
	if !strings.Contains(instructions, "live investigator in a shared Landfall war room") {
		t.Errorf("instructions missing or wrong: %q", instructions)
	}

	// 2. tools/list — the full 15-tool bridge surface, built over the session
	//    this command created.
	list := result(t, h.rpc(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	raw, ok := list["tools"].([]any)
	if !ok {
		t.Fatalf("no tools array in %v", list)
	}
	if len(raw) != 15 {
		t.Errorf("tools/list returned %d tools, want 15", len(raw))
	}
	names := map[string]bool{}
	for _, entry := range raw {
		tool, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("tool entry is not an object: %v", entry)
		}
		name, _ := tool["name"].(string)
		if name == "" {
			t.Errorf("tool with no name: %v", tool)
		}
		if _, ok := tool["inputSchema"].(map[string]any); !ok {
			t.Errorf("tool %q has no inputSchema", name)
		}
		names[name] = true
	}
	for _, want := range []string{"join_war_room", "get_brief", "get_updates", "post_finding", "post_widget"} {
		if !names[want] {
			t.Errorf("tools/list is missing %q", want)
		}
	}

	// 3. A tool call reaches the session's client — proof the tools were built
	//    over THE session serve joined, not a fresh empty one (which would
	//    answer "not connected" instead).
	call := result(t, h.rpc(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"note","arguments":{"text":"hello"}}}`))
	if call["isError"] == true {
		t.Errorf("note reported an error against a joined session: %v", call)
	}

	// 4. stdout is the wire and nothing else: serve must have redirected ui.Out
	//    to io.Discard, so a stray ui.Outf anywhere in the command (or in
	//    anything it calls) is inert rather than a corrupt JSON-RPC frame.
	if h.ui.Out != io.Discard {
		t.Errorf("serve left ui.Out = %T, want io.Discard — stdout is the MCP wire", h.ui.Out)
	}

	// 5. The hook query socket bound, and answers about THIS session.
	assertSocketAnswers(t, h, "inc-1")

	// 6. The join happened once, through the injected factory.
	if joined, _, _ := h.edge.counts(); joined != 1 {
		t.Errorf("join called %d times, want 1", joined)
	}

	// 7. Closing stdin ends the loop cleanly — exit 0.
	if err := h.closeStdin(); err != nil {
		t.Errorf("serve returned %v after stdin closed, want nil", err)
	}
	if _, left, _ := h.edge.counts(); left != 1 {
		t.Errorf("leave called %d times on shutdown, want 1", left)
	}
}

func assertSocketAnswers(t *testing.T, h *serveHarness, wantIncident string) {
	t.Helper()
	socks := hooks.ListHookSockets(h.ws)
	if len(socks) != 1 {
		t.Fatalf("expected exactly one bound hook socket, got %v (stderr:\n%s)", socks, h.errBuf.String())
	}
	res, err := hooks.SendToSocket(socks[0], hooks.StatusRequest(), 2*time.Second)
	if err != nil {
		t.Fatalf("hook socket status: %v", err)
	}
	if !res.OK {
		t.Fatalf("hook socket answered not-ok: %+v", res)
	}
	if res.IncidentID != wantIncident {
		t.Errorf("hook socket reports incident %q, want %q", res.IncidentID, wantIncident)
	}
}

// TestServeRunsUnjoinedWhenNoConfigResolves is the "started outside a resolved
// config" path (bin/landfall.mjs:463). serve must keep running so the agent can
// call join_war_room itself over the loop that is about to start — exiting here
// is the failure, not the unjoined state.
func TestServeRunsUnjoinedWhenNoConfigResolves(t *testing.T) {
	h := startServe(t, harnessOptions{cfg: nil})

	if !strings.Contains(h.errBuf.String(), "not joined yet") {
		t.Errorf("expected the not-joined notice on stderr; got:\n%s", h.errBuf.String())
	}
	if joined, _, _ := h.edge.counts(); joined != 0 {
		t.Errorf("join called %d times with no resolved config, want 0", joined)
	}

	// The MCP surface is fully live regardless — that is the entire point.
	list := result(t, h.rpc(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if tools, _ := list["tools"].([]any); len(tools) != 15 {
		t.Errorf("un-joined serve exposed %d tools, want 15", len(tools))
	}

	// And a room-write fails closed with the join instruction rather than
	// panicking on a nil client.
	call := result(t, h.rpc(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_brief","arguments":{}}}`))
	if call["isError"] != true {
		t.Fatalf("expected an isError result from an unjoined get_brief, got %v", call)
	}
	body, _ := json.Marshal(call["content"])
	if !strings.Contains(string(body), "not connected") {
		t.Errorf("unjoined error should name the fix; got %s", body)
	}

	if err := h.closeStdin(); err != nil {
		t.Errorf("serve returned %v, want nil", err)
	}
}

// --- shutdown ---------------------------------------------------------------

// TestServeShutsDownOnSIGTERM sends a REAL signal to this process and asserts
// serve unwinds and returns nil (exit 0), the way bin/landfall.mjs's handler
// calls process.exit(0).
//
// The test registers its own SIGTERM handler for its whole duration, so the
// signal can never fall through to the default disposition and kill the test
// binary in the window after serve's own signal.NotifyContext is released.
func TestServeShutsDownOnSIGTERM(t *testing.T) {
	guard := make(chan os.Signal, 1)
	signal.Notify(guard, syscall.SIGTERM)
	defer signal.Stop(guard)

	cfg := &client.Config{BaseURL: "https://landfall.test", Slug: "acme", IncidentID: "inc-2", Token: "t"}
	h := startServe(t, harnessOptions{cfg: cfg})

	socks := hooks.ListHookSockets(h.ws)
	if len(socks) != 1 {
		t.Fatalf("expected one bound socket before the signal, got %v", socks)
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("kill: %v", err)
	}

	if err := h.waitDone(); err != nil {
		t.Errorf("serve returned %v on SIGTERM, want nil (exit 0)", err)
	}

	// The full shutdown sequence ran: the room was left, and the socket node is
	// gone rather than left behind for the next process to trip over.
	if _, left, _ := h.edge.counts(); left != 1 {
		t.Errorf("leave called %d times on SIGTERM, want 1", left)
	}
	if remaining := hooks.ListHookSockets(h.ws); len(remaining) != 0 {
		t.Errorf("hook socket not cleaned up on shutdown: %v", remaining)
	}
	// The leave must survive the cancellation that caused it — a leave sent on
	// the already-cancelled process context never reaches the server, and the
	// room keeps a ghost participant until the presence timeout.
	if err := h.stdin.Close(); err != nil {
		t.Errorf("closing the test's stdin pipe: %v", err)
	}
}

// TestServeShutsDownOnContextCancel is the same unwind driven by a cancelled
// context rather than a signal — the path an embedding caller (and Cobra's own
// command context) takes.
func TestServeShutsDownOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cfg := &client.Config{BaseURL: "https://landfall.test", Slug: "acme", IncidentID: "inc-3", Token: "t"}
	h := startServe(t, harnessOptions{cfg: cfg, ctx: ctx})

	cancel()

	if err := h.waitDone(); err != nil {
		t.Errorf("serve returned %v on context cancel, want nil", err)
	}
	if _, left, _ := h.edge.counts(); left != 1 {
		t.Errorf("leave called %d times on cancel, want 1", left)
	}
	_ = h.stdin.Close()
}

// --- the pieces serve owns --------------------------------------------------

// TestServeKeepsPresenceAlive asserts the heartbeat half of keepLive actually
// started for the config resolved at startup — the half that has no MCP
// visibility at all and would otherwise fail silently.
func TestServeKeepsPresenceAlive(t *testing.T) {
	cfg := &client.Config{BaseURL: "https://landfall.test", Slug: "acme", IncidentID: "inc-4", Token: "t"}
	h := startServe(t, harnessOptions{cfg: cfg})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, beats := h.edge.counts(); beats > 0 {
			if err := h.closeStdin(); err != nil {
				t.Errorf("serve returned %v, want nil", err)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	_ = h.closeStdin()
	t.Fatal("no presence heartbeat within 3s of a joined serve")
}

// TestServeDoorbellRingsOnLiveEventEdge drives the realtime seam with a stub
// watcher and asserts serve's own enqueue/doorbell policy: the marker is
// written on the 0 → non-empty edge and NOT on the event after it.
//
// The transport is stubbed (T057-T058 replaces noopWatcher with the real
// Socket.IO client); the policy under test is serve's, not the transport's.
func TestServeDoorbellRingsOnLiveEventEdge(t *testing.T) {
	dir, runDir := shortTempDir(t), shortTempDir(t)
	ws := hooks.Workspace{Cwd: dir, Env: map[string]string{"XDG_RUNTIME_DIR": runDir}}

	events := make(chan client.Event, 4)
	watcher := &stubWatcher{events: events}

	edge := &fakeEdge{}
	errBuf := &lockedBuffer{}
	ui := &UI{Out: &bytes.Buffer{}, Err: errBuf}

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() { _, _ = io.Copy(io.Discard, outR) }()

	done := make(chan error, 1)
	cfg := client.Config{BaseURL: "https://landfall.test", Slug: "acme", IncidentID: "inc-5", Token: "t"}
	go func() {
		done <- runServe(context.Background(), ui, "", serveOptions{
			In:                inR,
			Out:               outW,
			Workspace:         ws,
			Watcher:           watcher,
			HeartbeatInterval: time.Hour,
			Resolve:           func(context.Context, *UI, string) (*client.Config, error) { return &cfg, nil },
			NewClient:         func(client.Config) session.EdgeClient { return edge },
		})
	}()

	waitFor(t, func() bool { return watcher.started() }, "the live watch to start")

	marker := hooks.DoorbellPath(dir)
	seq := func(n int64) *int64 { return &n }

	events <- client.Event{Seq: seq(1), Type: "finding.posted"}
	waitFor(t, func() bool { return countLines(marker) == 1 }, "the doorbell to ring on the first event")

	// A session that already owes context must NOT be re-rung: it will be shown
	// this event too when the bell it has yet to answer is answered.
	events <- client.Event{Seq: seq(2), Type: "finding.posted"}
	waitFor(t, func() bool { return len(readMarker(marker)) > 0 }, "the second event to be handled")
	time.Sleep(50 * time.Millisecond)
	if n := countLines(marker); n != 1 {
		t.Errorf("doorbell rang %d times across two events, want 1 (the 0 → non-empty edge only)", n)
	}

	_ = inW.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serve returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return")
	}
	if !watcher.stopped() {
		t.Error("the live watch was not stopped on shutdown")
	}
}

// stubWatcher stands in for internal/realtime until T057 lands.
type stubWatcher struct {
	events chan client.Event

	mu        sync.Mutex
	begun     bool
	unwatched bool
}

func (w *stubWatcher) Watch(ctx context.Context, _ client.Config, onEvent func(client.Event)) func() {
	w.mu.Lock()
	w.begun = true
	w.mu.Unlock()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case evt := <-w.events:
				onEvent(evt)
			}
		}
	}()
	return func() {
		w.mu.Lock()
		w.unwatched = true
		w.mu.Unlock()
	}
}

func (w *stubWatcher) started() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.begun
}

func (w *stubWatcher) stopped() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.unwatched
}

func readMarker(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func countLines(path string) int {
	body := strings.TrimSpace(readMarker(path))
	if body == "" {
		return 0
	}
	return len(strings.Split(body, "\n"))
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestServeDoorbellIsSelfIgnoring guards the promise the doorbell makes about
// the user's repository: its directory ignores itself, so the marker never
// shows up in `git status`.
func TestServeDoorbellIsSelfIgnoring(t *testing.T) {
	cfg := &client.Config{BaseURL: "https://landfall.test", Slug: "acme", IncidentID: "inc-6", Token: "t"}
	h := startServe(t, harnessOptions{cfg: cfg})
	defer func() { _ = h.closeStdin() }()

	// The doorbell directory is created lazily, on the first ring, so this
	// asserts the PATH serve chose rather than a directory that exists yet.
	want := filepath.Join(h.dir, hooks.DoorbellDir, hooks.DoorbellFile)
	if got := hooks.DoorbellPath(h.dir); got != want {
		t.Errorf("doorbell path = %q, want %q", got, want)
	}
}

// TestSetVersionIgnoresEmpty keeps an un-stamped build honest: reporting an
// empty serverInfo.version would look like a broken server rather than an
// unreleased one.
func TestSetVersionIgnoresEmpty(t *testing.T) {
	original := buildVersion
	defer func() { buildVersion = original }()

	SetVersion("")
	if buildVersion != original {
		t.Errorf("SetVersion(\"\") changed the version to %q", buildVersion)
	}
	SetVersion("9.9.9")
	if buildVersion != "9.9.9" {
		t.Errorf("SetVersion did not take: %q", buildVersion)
	}
	if got := (serveOptions{}).withDefaults().Version; got != "9.9.9" {
		t.Errorf("serveOptions defaulted the version to %q, want the build version", got)
	}
}
