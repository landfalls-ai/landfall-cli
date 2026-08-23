package cli

// hookevents_test.go — the WIRE + CLI layer of `test/hooks/stop-hook.test.mjs`
// and `test/hooks/cursor-adapter.test.mjs`, plus the `hooks pre-tool-use` half
// of `test/hooks/prod-policy-cli.test.mjs`.
//
// These assert what the HOST sees: the exit code, what landed on stdout, what
// landed on stderr. The failure mode of getting any of them wrong is silent —
// an entry sits in the user's config, fires on every turn, and its verdict is
// discarded (or, worse, its empty stdout is a JSON parse error).
//
// A real socket, a real bridge session and a real policy file throughout; only
// the two process streams and stdin are swapped, since a test cannot inherit the
// harness's own.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/hooks"
	"github.com/landfalls-ai/landfall-cli/internal/session"
)

// captureStreams swaps the package's stdout/stderr writers and the hook
// entrypoint's stdin for the duration of one test.
func captureStreams(t *testing.T, input string) (out, errOut *bytes.Buffer) {
	t.Helper()
	savedOut, savedErr, savedIn := stdout, stderr, hookEventStdin
	t.Cleanup(func() { stdout, stderr, hookEventStdin = savedOut, savedErr, savedIn })
	out, errOut = &bytes.Buffer{}, &bytes.Buffer{}
	stdout, stderr = out, errOut
	hookEventStdin = strings.NewReader(input)
	return out, errOut
}

// hookUI is a UI wired to the same buffers captureStreams installed.
func hookUI() *UI { return &UI{Out: stdout, Err: stderr} }

// exitCodeOf turns a command's returned error into the process exit code the
// host would see.
func exitCodeOf(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var e *exitError
	if !errors.As(err, &e) {
		t.Fatalf("unexpected error: %v", err)
	}
	return e.code
}

// hookSession is a bridge session with n queued room events, bound to a real
// socket in ws.
func hookSession(t *testing.T, ws hooks.Workspace, pid, n int) (*session.Session, *hooks.BoundSocket) {
	t.Helper()
	s := session.New(session.Options{Client: client.New(client.Config{IncidentID: "inc-1", Slug: "acme"}, nil)})
	// Clean + not dirty, so a peek settles attention from memory and never
	// reaches for a network this test does not have.
	s.SetAttention(&client.Attention{})
	s.SetAttentionDirty(false)
	for i := 0; i < n; i++ {
		seq := int64(i + 1)
		s.EnqueueEvent(context.Background(), client.Event{
			Seq: &seq, Type: "edge.finding",
			Payload: map[string]any{"displayName": "Ana", "text": "origin 5xx spiking on shard 0"},
		})
	}
	bound := hooks.StartHookSocket(context.Background(), s, hooks.StartOptions{
		Workspace: ws, PID: pid,
		Consume: func(sess hooks.SocketSession, upTo int64) int64 {
			return sess.(*session.Session).ConsumeUpTo(upTo)
		},
	})
	if bound == nil {
		t.Fatal("expected the socket to bind")
	}
	t.Cleanup(func() { _ = bound.Close() })
	return s, bound
}

// withHookWorkspace points the process at an isolated workspace: XDG_RUNTIME_DIR
// for the sockets, and the cwd (which hooks.Workspace{} resolves from) for the
// doorbell.
func withHookWorkspace(t *testing.T) hooks.Workspace {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "lf-hookcli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	t.Setenv("XDG_RUNTIME_DIR", root)

	saved, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(saved) })

	// The cwd a real hook resolves to may differ from `root` by a symlink
	// (/tmp → /private/tmp on macOS), and WorkspaceKey hashes the resolved path
	// on both sides — so ask the process the same question the hook will.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return hooks.Workspace{Cwd: cwd, Env: map[string]string{"XDG_RUNTIME_DIR": root}}
}

// --- stop, exit2 -------------------------------------------------------------

func TestHooksStopBlocksTheRealCommandWhenRoomContextIsOwed(t *testing.T) {
	ws := withHookWorkspace(t)
	s, _ := hookSession(t, ws, 5150, 2)

	out, errOut := captureStreams(t, "{}")
	code := exitCodeOf(t, runHookEventCommand(context.Background(), hookUI(), "stop", []string{"stop"}))

	if code != 2 {
		t.Fatalf("exit %d", code)
	}
	if out.String() != "" {
		t.Fatalf("stdout is the host's channel — the hook must not write to it: %q", out.String())
	}
	for _, want := range []string{"Do not conclude yet — 2 update(s)", "#1 edge.finding [Ana]"} {
		if !strings.Contains(errOut.String(), want) {
			t.Fatalf("stderr is missing %q:\n%s", want, errOut.String())
		}
	}
	// The refusal is fed verbatim back to the model — no "[landfall] " prefix.
	if strings.Contains(errOut.String(), "[landfall]") {
		t.Fatalf("the refusal must not be prefixed:\n%s", errOut.String())
	}

	// The block consumed them, so an unchanged room lets the next Stop through.
	if len(s.Pending()) != 0 {
		t.Fatalf("pending %d", len(s.Pending()))
	}
	out, errOut = captureStreams(t, "{}")
	code = exitCodeOf(t, runHookEventCommand(context.Background(), hookUI(), "stop", []string{"stop"}))
	if code != 0 || out.String() != "" || errOut.String() != "" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out.String(), errOut.String())
	}
}

func TestHooksStopWithTheLoopGuardSetNeverBlocksEvenWithContextOwed(t *testing.T) {
	ws := withHookWorkspace(t)
	s, _ := hookSession(t, ws, 5151, 3)

	_, errOut := captureStreams(t, `{"stop_hook_active":true}`)
	code := exitCodeOf(t, runHookEventCommand(context.Background(), hookUI(), "stop", []string{"stop"}))

	if code != 0 || errOut.String() != "" {
		t.Fatalf("exit %d, stderr %q", code, errOut.String())
	}
	if len(s.Pending()) != 3 {
		t.Fatalf("a guarded Stop must not consume either, pending %d", len(s.Pending()))
	}
}

func TestHooksStopExitsZeroWithNoStdinAtAll(t *testing.T) {
	withHookWorkspace(t)
	out, _ := captureStreams(t, "")
	code := exitCodeOf(t, runHookEventCommand(context.Background(), hookUI(), "stop", []string{"stop"}))
	if code != 0 || out.String() != "" {
		t.Fatalf("exit %d, stdout %q", code, out.String())
	}
}

// --- stop, cursor-json -------------------------------------------------------

func TestHooksStopHostCursorWritesJSONToStdoutAndExitsZero(t *testing.T) {
	ws := withHookWorkspace(t)
	s, _ := hookSession(t, ws, 6100, 2)

	out, errOut := captureStreams(t, "{}")
	argv := []string{"stop", "--host", "cursor"}
	code := exitCodeOf(t, runHookEventCommand(context.Background(), hookUI(), "stop", argv))

	if code != 0 {
		t.Fatalf("a non-zero exit reads as a broken hook to Cursor: %d", code)
	}
	if errOut.String() != "" {
		t.Fatalf("Cursor never shows the model stderr: %q", errOut.String())
	}
	var verdict struct {
		FollowupMessage string `json:"followup_message"`
	}
	if err := json.Unmarshal(out.Bytes(), &verdict); err != nil {
		t.Fatalf("stdout must be one parseable JSON object, got %q: %v", out.String(), err)
	}
	for _, want := range []string{"Do not conclude yet — 2 update(s)", "#1 edge.finding [Ana]"} {
		if !strings.Contains(verdict.FollowupMessage, want) {
			t.Fatalf("missing %q:\n%s", want, verdict.FollowupMessage)
		}
	}

	// Consumed, so an unchanged room lets the next stop through — with `{}`, not
	// with silence.
	if len(s.Pending()) != 0 {
		t.Fatalf("pending %d", len(s.Pending()))
	}
	out, _ = captureStreams(t, "{}")
	code = exitCodeOf(t, runHookEventCommand(context.Background(), hookUI(), "stop", argv))
	if code != 0 || strings.TrimSpace(out.String()) != "{}" {
		t.Fatalf("exit %d, stdout %q", code, out.String())
	}
}

func TestTheExit2HostsAreUntouchedByTheFlagExisting(t *testing.T) {
	ws := withHookWorkspace(t)
	hookSession(t, ws, 6101, 1)

	out, errOut := captureStreams(t, "{}")
	code := exitCodeOf(t, runHookEventCommand(context.Background(), hookUI(), "stop", []string{"stop"}))
	if code != 2 || out.String() != "" {
		t.Fatalf("exit %d, stdout %q", code, out.String())
	}
	if !strings.Contains(errOut.String(), "Do not conclude yet") {
		t.Fatalf("stderr %q", errOut.String())
	}
}

func TestAnUnknownHostAnswersOnTheConventionTwoOfThreeHostsShare(t *testing.T) {
	ws := withHookWorkspace(t)
	hookSession(t, ws, 6102, 1)

	_, errOut := captureStreams(t, "{}")
	argv := []string{"stop", "--host", "not-a-host"}
	code := exitCodeOf(t, runHookEventCommand(context.Background(), hookUI(), "stop", argv))
	if code != 2 {
		t.Fatalf("an unrecognized --host must not be fatal, and must not silently allow: %d", code)
	}
	if !strings.Contains(errOut.String(), "Do not conclude yet") {
		t.Fatalf("stderr %q", errOut.String())
	}
}

// --- user-prompt-submit ------------------------------------------------------

func TestHooksUserPromptSubmitWritesTheInjectionObjectToStdout(t *testing.T) {
	ws := withHookWorkspace(t)
	s, _ := hookSession(t, ws, 6200, 1)

	out, _ := captureStreams(t, "{}")
	code := exitCodeOf(t, runHookEventCommand(context.Background(), hookUI(), "user-prompt-submit", []string{"user-prompt-submit"}))
	if code != 0 {
		t.Fatalf("this event must never block a prompt: exit %d", code)
	}
	var payload struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("stdout %q: %v", out.String(), err)
	}
	if payload.HookSpecificOutput.HookEventName != "UserPromptSubmit" {
		t.Fatalf("got %q", payload.HookSpecificOutput.HookEventName)
	}
	if !strings.Contains(payload.HookSpecificOutput.AdditionalContext, "#1 edge.finding [Ana]") {
		t.Fatalf("got %q", payload.HookSpecificOutput.AdditionalContext)
	}
	// Delivered, and only now may the cursor move.
	if s.Cursor() != 1 {
		t.Fatalf("cursor %d", s.Cursor())
	}
}

func TestHooksUserPromptSubmitSaysNothingWhenNothingIsOwed(t *testing.T) {
	withHookWorkspace(t)
	out, errOut := captureStreams(t, "{}")
	code := exitCodeOf(t, runHookEventCommand(context.Background(), hookUI(), "user-prompt-submit", []string{"user-prompt-submit"}))
	if code != 0 || out.String() != "" || errOut.String() != "" {
		t.Fatalf("every prompt runs this hook — silence is the common case: exit %d, %q / %q",
			code, out.String(), errOut.String())
	}
}

// --- file-changed ------------------------------------------------------------

func TestHooksFileChangedWritesNothingToStdoutAndNudgesOnStderr(t *testing.T) {
	ws := withHookWorkspace(t)
	s, _ := hookSession(t, ws, 6300, 2)

	out, errOut := captureStreams(t, "{}")
	code := exitCodeOf(t, runHookEventCommand(context.Background(), hookUI(), "file-changed", []string{"file-changed"}))

	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if out.String() != "" {
		t.Fatalf("nothing may be written to a channel the host ignores: %q", out.String())
	}
	if !strings.Contains(errOut.String(), "2 update(s) from your war room") {
		t.Fatalf("stderr %q", errOut.String())
	}
	// The wake consumes NOTHING.
	if s.Cursor() != -1 || len(s.Pending()) != 2 {
		t.Fatalf("cursor %d, pending %d", s.Cursor(), len(s.Pending()))
	}
	if hooks.ReadStage(ws) == nil {
		t.Fatal("the wake must stage for user-prompt-submit to deliver")
	}
}

// --- pre-tool-use ------------------------------------------------------------

// recordingAPI is a stand-in Landfall that records every request it receives.
func recordingAPI(t *testing.T) (baseURL string, requests *[]string) {
	t.Helper()
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = append(got, r.URL.Path+" "+string(body))
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"decision":"none"}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &got
}

func writeHookPolicy(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, ".config", "landfall")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"version":1,"rules":[{"id":"kubectl-prod","description":"kubectl aimed at production",` +
		`"command":"kubectl","allOf":["--context=prod"],"category":"kubernetes","entityHints":["prod-cluster"]}]}`
	if err := os.WriteFile(filepath.Join(dir, "prod-policy.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAMatchedCommandSendsNothingWhenThereIsNoTerminalToConfirmOn(t *testing.T) {
	// The unit tests inject a fake Confirm, so they prove the handler's logic.
	// This proves the thing that logic exists for, with nothing stubbed: a hook
	// running in a test harness has no controlling terminal it can read a `y`
	// from, therefore nobody can have confirmed, therefore the socket must stay
	// silent. If that ever stops being true, the request count here goes to 1.
	home := installSandbox(t)
	seedSession(t)
	writeHookPolicy(t, home)
	baseURL, requests := recordingAPI(t)
	t.Setenv("LANDFALL_BASE_URL", baseURL)

	input := `{"tool_name":"Bash","tool_input":{"command":"kubectl --context=prod get secret db-root -o yaml"}}`
	out, _ := captureStreams(t, input)
	code := exitCodeOf(t, runHookEventCommand(context.Background(), hookUI(), "pre-tool-use", []string{"pre-tool-use"}))

	if code != 0 {
		t.Fatalf("a PreToolUse hook must never block the tool call: exit %d", code)
	}
	if out.String() != "" {
		t.Fatalf("stdout belongs to the host: %q", out.String())
	}
	if len(*requests) != 0 {
		t.Fatalf("an unconfirmed intent reached the network: %v", *requests)
	}
}

func TestANonMatchingCommandExitsZeroSilentlyAndTouchesNothing(t *testing.T) {
	home := installSandbox(t)
	seedSession(t)
	writeHookPolicy(t, home)
	baseURL, requests := recordingAPI(t)
	t.Setenv("LANDFALL_BASE_URL", baseURL)

	out, errOut := captureStreams(t, `{"tool_name":"Bash","tool_input":{"command":"git status"}}`)
	code := exitCodeOf(t, runHookEventCommand(context.Background(), hookUI(), "pre-tool-use", []string{"pre-tool-use"}))

	if code != 0 || out.String() != "" {
		t.Fatalf("exit %d, stdout %q", code, out.String())
	}
	if errOut.String() != "" {
		t.Fatalf("a non-matching command should not say anything at all: %q", errOut.String())
	}
	if len(*requests) != 0 {
		t.Fatalf("requests %v", *requests)
	}
}

// --- dispatch ----------------------------------------------------------------

func TestTheHooksDispatcherOwnsEveryRegisteredEventAndPolicy(t *testing.T) {
	// The seam hooksinstall.go declares: anything this file does not claim falls
	// through to the usage line, which is what an unrecognized subcommand must
	// still get.
	//
	// Sandboxed, because "was it handled?" is answered by actually running it —
	// an isolated workspace (no serve process, so every handler takes its
	// nothing-owed path) and an isolated HOME (so `policy` reads no real file).
	installSandbox(t)
	withHookWorkspace(t)
	captureStreams(t, "")

	for _, sub := range append(hooks.HookEventIDs(), "policy") {
		handled, err := hooksEventDispatch(hookUI(), sub, []string{sub})
		if !handled {
			t.Fatalf("%q must be handled here", sub)
		}
		if code := exitCodeOf(t, err); code != 0 {
			t.Fatalf("%q on a clean machine: exit %d", sub, code)
		}
	}
	if handled, _ := hooksEventDispatch(hookUI(), "nonsense", []string{"nonsense"}); handled {
		t.Fatal("an unrecognized subcommand must fall through to the usage line")
	}
	// install/uninstall stay with their own track: this dispatcher must not claim
	// them, or hooksinstall.go's earlier switch would be unreachable.
	for _, sub := range []string{"install", "uninstall"} {
		if handled, _ := hooksEventDispatch(hookUI(), sub, []string{sub}); handled {
			t.Fatalf("%q belongs to hooksinstall.go", sub)
		}
	}
}
