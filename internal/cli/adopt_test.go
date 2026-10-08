package cli

// adopt_test.go — the dual connection (live.md FR-L5), end to end on this
// machine: a real daemon on a real socket, a real `serve` front end over
// pipes, the real `landfall join --incident` wiring against a fake Landfall
// that mints and redeems a share link and answers the agent's reads.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/daemon"
	"github.com/landfalls-ai/landfall-cli/internal/hooks"
)

// notConnected is the tools package's unjoined error, verbatim: the text an
// agent has always been given when it calls a room tool before joining.
const notConnected = "not connected — call join_war_room with a Landfall agent share link first"

const adoptFrame = `{"asOfSeq":40,"version":1,
 "incident":{"displayId":"Acme 7","title":"web-edge 5xx","severity":"SEV2","status":"investigating"},
 "brief":{"established":[],"workingTheory":[],"disproved":[],"open":[]},"participants":[]}`

// modLandfall is a Landfall that mints a link for inc-1 with the person's
// sign-in, redeems it once for the room's session, and answers the agent's
// frame read with that session.
func modLandfall(t *testing.T) *fakeLandfall {
	t.Helper()
	f := newFakeLandfall(t)
	f.handle("POST /o/acme/incidents/inc-1/edge/share-link", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("authorization") != "Bearer "+personToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusCreated)
		writeJSON200(w, map[string]any{"shareUrl": f.srv.URL + "/j/code123", "expiresAt": "2026-10-08T17:00:00Z"})
	})
	var redeemed int32
	f.handle("POST /j/code123/redeem", func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&redeemed, 1) > 1 {
			w.WriteHeader(http.StatusGone) // single-use, as the server is
			return
		}
		writeJSON200(w, map[string]any{"token": roomToken, "humanActorId": "user-1", "slug": "acme", "incidentId": "inc-1", "kind": "member"})
	})
	f.handle("GET /o/acme/incidents/inc-1/edge/context/frame", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("authorization") != "Bearer "+roomToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(adoptFrame))
	})
	return f
}

// joinFromMod is `landfall join --incident inc-1 --host claude-code` in ws,
// through the production daemon wiring.
func joinFromMod(t *testing.T, f *fakeLandfall, ws hooks.Workspace) map[string]any {
	t.Helper()
	deps := joinIncidentRoomDeps(ws, hooks.HarnessFromClientName("claude-code"), func(string) {}, func(ctx context.Context, link string) (*client.Config, error) {
		cfg, err := client.RedeemShareLink(ctx, link, client.RedeemOptions{})
		if err != nil {
			return nil, err
		}
		return &cfg, nil
	})
	read := f.deps(true)
	deps.Org = read.Org
	ans := roundTrip(t, RunJoinIncident(context.Background(), "inc-1", deps))
	if ans["ok"] != true {
		t.Fatalf("join --incident: %v", ans)
	}
	return ans
}

// launchServe starts a `serve` in daemon mode with no link. Its own redeem
// counts calls and refuses: adoption must never redeem.
func launchServe(t *testing.T, ws hooks.Workspace, noWatch bool, redeems *int32) *frontEndRun {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	r := &frontEndRun{stdin: inW, out: bufio.NewReader(outR), errBuf: &lockedBuffer{}, done: make(chan error, 1)}
	ui := &UI{Out: &bytes.Buffer{}, Err: r.errBuf}
	so := serveOptions{
		In: inR, Out: outW, Workspace: ws,
		Resolve:      func(context.Context, *UI, string) (*client.Config, error) { return nil, nil },
		EnsureDaemon: func(hooks.Workspace, func(string)) bool { return true },
		Redeem: func(context.Context, string) (client.Config, error) {
			atomic.AddInt32(redeems, 1)
			return client.Config{}, fmt.Errorf("could not join the war room (HTTP 410): the link expired or was already used")
		},
		noAdoptWatch: noWatch,
	}
	go func() {
		err := runServe(context.Background(), ui, "", so)
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
	r.waitFor(t, "MCP stdio server ready")
	return r
}

// callTool calls a tool with arguments and returns its result object.
func (r *frontEndRun) callTool(t *testing.T, id int, tool string, args map[string]any) map[string]any {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	a, _ := json.Marshal(args)
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, id, tool, a)
	if _, err := io.WriteString(r.stdin, body+"\n"); err != nil {
		t.Fatal(err)
	}
	line, err := r.out.ReadBytes('\n')
	if err != nil {
		t.Fatalf("no answer to %s: %v", tool, err)
	}
	var res struct {
		Result map[string]any `json:"result"`
	}
	if json.Unmarshal(line, &res) != nil || res.Result == nil {
		t.Fatalf("%s answered %s", tool, line)
	}
	return res.Result
}

func resultText(res map[string]any) string {
	var b strings.Builder
	content, _ := res["content"].([]any)
	for _, c := range content {
		if m, ok := c.(map[string]any); ok {
			s, _ := m["text"].(string)
			b.WriteString(s)
		}
	}
	return b.String()
}

// agentReaders are the connected agent readers of this workspace the daemon
// holds, by harness.
func agentReaders(t *testing.T, ws hooks.Workspace) []*daemon.Reader {
	t.Helper()
	res, err := daemon.Send(hooks.DaemonSocketPath(ws), daemon.Request{Op: "rooms"}, time.Second)
	if err != nil {
		t.Fatalf("rooms: %v", err)
	}
	var out []*daemon.Reader
	for _, room := range res.Rooms {
		for _, rd := range room.Readers {
			if rd.Kind == daemon.KindAgent && rd.Connected && rd.WorkspaceKey == hooks.WorkspaceKey(ws.Dir()) {
				out = append(out, rd)
			}
		}
	}
	return out
}

// The person joins from the mod; their agent's serve, idle in the same
// folder, is in the room a moment later with no paste and no tool call, on
// the seat the join already holds (one /edge/join, one redeem), and its tools
// then work.
func TestJoiningFromTheModPutsTheAgentInTheRoom(t *testing.T) {
	t.Setenv("LANDFALL_AGENT_LABEL", "")
	ws := liveWorkspace(t, "claude-code")
	joins := startTestDaemon(t, ws)
	f := modLandfall(t)
	var serveRedeems int32
	fe := launchServe(t, ws, false, &serveRedeems)
	fe.initialize(t, "claude-code")
	if got := agentReaders(t, ws); len(got) != 0 {
		t.Fatalf("nobody joined yet, but the daemon holds %v", got)
	}

	began := time.Now()
	joinFromMod(t, f, ws)
	fe.waitFor(t, "joined incident inc-1: the person put this folder in the room from the mod")
	t.Logf("join --incident started to agent in the room (mint, redeem, attach, adoption): %s", time.Since(began))
	if took := time.Since(began); took > 2*time.Second {
		t.Fatalf("the agent took %s to arrive", took)
	}
	got := agentReaders(t, ws)
	if len(got) != 1 || got[0].Harness != "claude-code" || got[0].Seat != "Claude Code" {
		t.Fatalf("one agent reader for this folder's Claude Code session: %+v", got)
	}
	labels, tokens := joins.snapshot()
	if strings.Join(labels, ",") != "Claude Code" || tokens[0] != roomToken {
		t.Fatalf("one /edge/join, by the person's join, with the room's session: %v %v", labels, tokens)
	}
	if n := len(f.callsTo("/j/code123/redeem")); n != 1 {
		t.Fatalf("the link must be redeemed once, got %d", n)
	}
	if n := atomic.LoadInt32(&serveRedeems); n != 0 {
		t.Fatalf("adoption must not redeem anything, serve redeemed %d time(s)", n)
	}

	// The watch's view: the agent is in the room.
	peek, err := daemon.Send(hooks.DaemonSocketPath(ws), daemon.Request{Op: "peek", WorkspaceKey: hooks.WorkspaceKey(ws.Dir()), Harness: "claude-code"}, 2*time.Second)
	if err != nil || len(peek.Rooms) != 1 || peek.Rooms[0].Agent == nil || !peek.Rooms[0].Agent.InRoom {
		t.Fatalf("peek: %v %+v", err, peek)
	}

	// And the agent's own tools work.
	brief := fe.callTool(t, 2, "get_brief", nil)
	if brief["isError"] == true || !strings.Contains(resultText(brief), "Acme 7") {
		t.Fatalf("get_brief after adoption: %v", brief)
	}
	if res := fe.callTool(t, 3, "get_updates", nil); res["isError"] == true {
		t.Fatalf("get_updates after adoption: %v", res)
	}
	if res := fe.callTool(t, 4, "share_with_room", map[string]any{"text": "p95 on origin-b doubled at 15:45Z"}); res["isError"] == true {
		t.Fatalf("share_with_room after adoption: %v", res)
	}
}

// With the background wait off, the next tool call is where the agent
// arrives: nothing before it, the call itself works.
func TestTheNextToolCallAdoptsTheRoom(t *testing.T) {
	t.Setenv("LANDFALL_AGENT_LABEL", "")
	ws := liveWorkspace(t, "claude-code")
	joins := startTestDaemon(t, ws)
	f := modLandfall(t)
	var serveRedeems int32
	fe := launchServe(t, ws, true, &serveRedeems)
	fe.initialize(t, "claude-code")
	joinFromMod(t, f, ws)
	time.Sleep(150 * time.Millisecond)
	if got := agentReaders(t, ws); len(got) != 0 {
		t.Fatalf("with no wait running, nothing joins before a tool call: %+v", got)
	}
	brief := fe.callTool(t, 2, "get_brief", nil)
	if brief["isError"] == true || !strings.Contains(resultText(brief), "Acme 7") {
		t.Fatalf("the first tool call adopts the room and answers: %v", brief)
	}
	if got := agentReaders(t, ws); len(got) != 1 {
		t.Fatalf("after the call: %+v", got)
	}
	if labels, _ := joins.snapshot(); len(labels) != 1 {
		t.Fatalf("no second /edge/join: %v", labels)
	}
	if n := atomic.LoadInt32(&serveRedeems); n != 0 || len(f.callsTo("/j/code123/redeem")) != 1 {
		t.Fatalf("redeems: serve %d, server %d", n, len(f.callsTo("/j/code123/redeem")))
	}
}

// Nothing on offer for this folder: the tool fails exactly as it always has,
// even while the person is in a room from another folder.
func TestNoRoomForThisFolderLeavesTheToolErrorUnchanged(t *testing.T) {
	t.Setenv("LANDFALL_AGENT_LABEL", "")
	ws := liveWorkspace(t, "claude-code")
	startTestDaemon(t, ws)
	f := modLandfall(t)
	var serveRedeems int32
	fe := launchServe(t, ws, false, &serveRedeems)
	fe.initialize(t, "claude-code")

	// Another checkout on the same machine (the same daemon) joins from the mod.
	other := hooks.Workspace{Cwd: shortTempDir(t), Env: ws.Env, Harness: "claude-code"}
	joinFromMod(t, f, other)
	time.Sleep(150 * time.Millisecond)

	res := fe.callTool(t, 2, "get_brief", nil)
	if res["isError"] != true || resultText(res) != "error: "+notConnected {
		t.Fatalf("an unjoined get_brief must fail as before (%q), got %v", notConnected, res)
	}
	if got := agentReaders(t, ws); len(got) != 0 {
		t.Fatalf("this folder's agent joined a room it was not put in: %+v", got)
	}
	if n := atomic.LoadInt32(&serveRedeems); n != 0 {
		t.Fatalf("serve redeemed %d time(s)", n)
	}
}

// A link pasted to one session is that session's join, not an offer: a second
// session opened in the same folder stays out until it is asked in.
func TestALinkJoinIsNotOfferedToANewSession(t *testing.T) {
	t.Setenv("LANDFALL_AGENT_LABEL", "")
	ws := liveWorkspace(t, "claude-code")
	startTestDaemon(t, ws)
	cfg := &client.Config{BaseURL: "http://127.0.0.1:1", Slug: "acme", IncidentID: "inc-1", Token: "edge-token"}
	a := startFrontEnd(t, ws, "https://app.landfalls.test/j/pasted", func(context.Context, *UI, string) (*client.Config, error) { c := *cfg; return &c, nil })
	a.initialize(t, "claude-code")
	a.waitFor(t, "joined incident inc-1")

	var serveRedeems int32
	b := launchServe(t, ws, false, &serveRedeems)
	b.initialize(t, "claude-code")
	time.Sleep(150 * time.Millisecond)
	res := b.callTool(t, 2, "get_brief", nil)
	if res["isError"] != true || resultText(res) != "error: "+notConnected {
		t.Fatalf("a second session must not ride a pasted link: %v", res)
	}
	if strings.Contains(b.errBuf.String(), "joined incident") {
		t.Fatalf("the second session joined:\n%s", b.errBuf.String())
	}
}
