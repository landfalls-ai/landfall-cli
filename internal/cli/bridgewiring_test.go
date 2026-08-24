package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/bridge"
	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/hooks"
	"github.com/landfalls-ai/landfall-cli/internal/mcp"
	"github.com/landfalls-ai/landfall-cli/internal/session"
	"github.com/landfalls-ai/landfall-cli/internal/spool"
	"github.com/landfalls-ai/landfall-cli/internal/tools"
)

// TestBridgeSwapsRecordActivityForShareWithRoom pins the SWAP, not the count.
//
// A count assertion is the wrong tool here — what matters is WHICH tools are
// present, and specifically the ordering constraint the senior review flagged:
// record_activity may only leave the main agent once the worker's descriptive
// activity lane exists to replace it. Otherwise the room gets a participant
// that acts and never speaks.
//
// (The surfaces are 15 without the bridge and 8 with it, since FR-001 moves the
// seven publish/vetting verbs to the worker.)
func TestBridgeSwapsRecordActivityForShareWithRoom(t *testing.T) {
	names := func(list []mcp.Tool) map[string]bool {
		out := map[string]bool{}
		for _, tl := range list {
			out[tl.Name] = true
		}
		return out
	}

	sess := session.New(session.Options{})

	// No queue => no worker => record_activity MUST stay, or nothing narrates.
	without := names(tools.Build(sess))
	if !without["record_activity"] {
		t.Error("record_activity removed with no worker to take over: the room would see a mute participant")
	}
	if without["share_with_room"] {
		t.Error("share_with_room offered with no queue behind it")
	}

	// With a queue, the worker owns the lane and the swap is correct.
	sp, err := spool.Open(bridgeWS(t).Getenv, "wskey")
	if err != nil {
		t.Fatalf("spool.Open: %v", err)
	}
	with := names(tools.BuildWithAccepter(sess, bridge.NewAccepter(sp, nil)))
	if !with["share_with_room"] {
		t.Error("share_with_room missing with a live queue")
	}
	if with["record_activity"] {
		t.Error("record_activity still on the main agent; the worker already owns the activity lane")
	}
}

// bridgeWS builds a workspace whose runtime AND durable state both live in
// temp dirs, so a test's spool never touches the developer's real
// ~/.local/state.
func bridgeWS(t *testing.T) hooks.Workspace {
	t.Helper()
	dir, runDir, stateDir := shortTempDir(t), shortTempDir(t), shortTempDir(t)
	return hooks.Workspace{Cwd: dir, Env: map[string]string{
		"XDG_RUNTIME_DIR": runDir,
		"XDG_STATE_HOME":  stateDir,
	}}
}

// TestBothJoinPathsRunTheSharedPostJoinWork is the regression guard for a bug
// the independent design review caught before it shipped.
//
// `serve` has TWO join paths: the agent calling join_war_room over MCP, and
// `serve --link` joining at startup. They used to duplicate the post-join work
// inline — serve.go's own comment admitted it — so anything wired only into
// the session's OnJoined callback would never run under `--link`, which is the
// common case for a responder following a share link. The bridge worker is
// exactly such a thing.
//
// The bug is invisible to any test that exercises only the MCP path, which is
// why it survived review of the code and was caught by reading it.
func TestBothJoinPathsRunTheSharedPostJoinWork(t *testing.T) {
	run := func(t *testing.T, incidentID string, viaStartup bool) {
		t.Helper()
		errBuf := &lockedBuffer{}
		ui := &UI{Out: &bytes.Buffer{}, Err: errBuf}

		inR, inW := io.Pipe()
		outR, outW := io.Pipe()
		go func() { _, _ = io.Copy(io.Discard, outR) }()

		cfg := client.Config{BaseURL: "https://landfall.test", Slug: "acme", IncidentID: incidentID, Token: "t"}
		edge := &fakeEdge{}
		opts := serveOptions{
			In: inR, Out: outW, Workspace: bridgeWS(t),
			Watcher:           noopWatcher{},
			HeartbeatInterval: time.Hour,
			NewClient:         func(client.Config) session.EdgeClient { return edge },
		}
		if viaStartup {
			opts.Resolve = func(context.Context, *UI, string) (*client.Config, error) { return &cfg, nil }
		} else {
			opts.Resolve = func(context.Context, *UI, string) (*client.Config, error) { return nil, nil }
			opts.Redeem = func(context.Context, string) (client.Config, error) { return cfg, nil }
		}

		done := make(chan error, 1)
		go func() { done <- runServe(context.Background(), ui, "", opts) }()

		if !viaStartup {
			_, _ = inW.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"join_war_room","arguments":{"shareUrl":"https://landfall.test/x?ticket=y"}}}` + "\n"))
		}

		waitFor(t, func() bool {
			return strings.Contains(errBuf.String(), "joined incident "+incidentID)
		}, "the join to complete")

		// THE assertion. An earlier version stopped at the log line above and
		// was useless: the duplicated startup path logged it too, so reverting
		// the fix still passed. Mutation testing caught that.
		//
		// The worker's sweep polls GetUpdates, so a rising count is the only
		// externally visible proof it is actually running on THIS path.
		waitFor(t, func() bool { return edge.updateCount() > 0 },
			"the bridge worker to start and sweep")

		_ = inW.Close()
		<-done
	}

	t.Run("startup --link path", func(t *testing.T) { run(t, "inc-startup", true) })
	t.Run("MCP join_war_room path", func(t *testing.T) { run(t, "inc-mcp", false) })
}

// TestShareWithRoomIsRegisteredWhenTheQueueOpens — FR-007: the bridge activates
// on join with no extra setup step. If the durable queue cannot open, the tool
// is omitted rather than registered over a broken queue (an agent told "shared"
// about a finding that went nowhere is worse than one without the verb), so
// this also pins that the happy path really does offer it.
func TestShareWithRoomIsRegisteredWhenTheQueueOpens(t *testing.T) {
	ui := &UI{Out: &bytes.Buffer{}, Err: &lockedBuffer{}}

	worker, accepter := startBridge(ui, bridgeWS(t))
	if worker == nil || accepter == nil {
		t.Fatal("bridge did not start in a writable workspace; share_with_room would be silently unavailable")
	}
	worker.Stop()
}

// TestBridgeDegradesWhenStateIsUnwritable — best-effort by design. A responder
// mid-incident needs MCP more than they need the background queue, so serve
// must keep working without it.
func TestBridgeDegradesWhenStateIsUnwritable(t *testing.T) {
	errBuf := &lockedBuffer{}
	ui := &UI{Out: &bytes.Buffer{}, Err: errBuf}

	// A state root that cannot be created: a path under a regular file.
	dir := shortTempDir(t)
	blocker := dir + "/not-a-dir"
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	ws := hooks.Workspace{Cwd: dir, Env: map[string]string{
		"XDG_RUNTIME_DIR": dir,
		"XDG_STATE_HOME":  blocker + "/nested",
	}}

	worker, accepter := startBridge(ui, ws)
	if worker != nil || accepter != nil {
		t.Fatal("bridge started against an unwritable state root")
	}
	if !strings.Contains(errBuf.String(), "share_with_room is unavailable") {
		t.Errorf("degradation was silent; the operator has no way to know the verb is missing. stderr: %q", errBuf.String())
	}
}
