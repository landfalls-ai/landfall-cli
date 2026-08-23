// run.go — the dispatch a registered hook entry actually reaches. A Go port of
// `src/hooks/run.mjs`'s `runHookEvent`.
//
// #222 shipped the installer and this contract; #225 filled in `stop`, #227
// `file-changed` + `user-prompt-submit`, and #233 `pre-tool-use`. Each speaks on
// a different channel, because each host event allows a different one:
//
//	stop          exit 2 + stderr — a REFUSAL every host understands. The agent
//	              is concluding, and the point is that it may not yet.
//	file-changed  exit 0, stderr only — a WAKE. The host discards this event's
//	              output entirely, so it stages the digest and nudges the human,
//	              and consumes nothing.
//	user-prompt-submit
//	              exit 0 + a structured JSON object on stdout — the INJECTION.
//	              The one event that can both run on resumption and be heard.
//	pre-tool-use  exit 0, always. In Claude Code a non-zero PreToolUse exit
//	              BLOCKS the tool call, and that hook exists to observe an
//	              investigation, not to gate one — an unreachable API, an expired
//	              session, a malformed policy file and a declined prompt are all
//	              "carry on". stdout stays empty because the host parses it;
//	              anything meant for the human goes to stderr.
//
// The shared default for an event with nothing to say is the one that is always
// safe, because the installer registers a command the host runs on every
// matching event from the moment it is written:
//
//	exit 0, print nothing = "nothing pending, carry on"
//
// ── HOW A HANDLER PLUGS IN ────────────────────────────────────────────────
// The four handlers are a separate track (T040-T044). Each owns its own file and
// registers itself from an init() there:
//
//	func init() { RegisterHookHandler("stop", runStopHook) }
//
// so this file never has to grow an import of, or a branch for, any of them.
// Until a handler registers, its event answers the protocol-correct no-op below
// — which is the right answer for a registered command whose handler has not
// shipped yet, not a crash in someone's agent turn.
package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// HookResult is what one hook run produces: the process exit code the host
// should see, plus whatever it wants written on each channel.
//
// 0 allows the turn to proceed, 2 blocks it with Stderr fed back to the model.
type HookResult struct {
	// ExitCode is the process exit code.
	ExitCode int
	// Stdout is the host's structured channel — written ONLY by a protocol that
	// parses it (Cursor's JSON, `user-prompt-submit`'s injection object). Must
	// stay empty under exit2.
	Stdout string
	// Stderr is everything meant for the model or the human.
	Stderr string
	// Error is set only for a dispatch-level failure (an unknown event id).
	Error string
	// Result is the handler's own outcome label, for tests and logging. Never
	// written to any stream.
	Result string
}

// HookDeps is everything a handler is given. Everything is injectable so a
// handler is testable without a serve process, a terminal or a war room.
type HookDeps struct {
	// Input is the hook payload the host wrote to stdin — READ ONCE by the
	// entrypoint (ReadHookInput) and passed to every handler. A handler must
	// never read stdin itself; a second independent read would race this one.
	Input string
	// Host is the id the registered command was written with (#228). It selects
	// the output protocol and nothing else.
	Host string
	// Workspace is which workspace's sockets, doorbell and stage to look at.
	Workspace Workspace
	// Log receives human-readable lines. A handler must route everything it
	// wants a human to see through this, never through fmt.Print — stdout is a
	// wire format in three of the four events.
	Log func(format string, args ...any)
	// Now supplies the clock. Nil means time.Now.
	Now func() time.Time
}

// Logf is Log, with a nil-safe default.
func (d HookDeps) Logf(format string, args ...any) {
	if d.Log != nil {
		d.Log(format, args...)
	}
}

// Clock is Now, with a nil-safe default.
func (d HookDeps) Clock() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// HookHandler runs one hook event.
type HookHandler func(ctx context.Context, deps HookDeps) HookResult

var (
	handlersMu sync.RWMutex
	handlers   = map[string]HookHandler{}
)

// RegisterHookHandler installs the handler for one event id. Called from an
// init() in the handler's own file (T040-T044), so this file needs no knowledge
// of any handler.
//
// Panics on an unknown event id or a double registration: both are programmer
// errors visible at process start, and a hook that silently registered against a
// typo'd event would simply never run — the failure mode #222 already shipped
// once with a matcher that never fired.
func RegisterHookHandler(eventID string, h HookHandler) {
	if FindHookEvent(eventID) == nil {
		panic("hooks: RegisterHookHandler for unknown event id " + eventID)
	}
	handlersMu.Lock()
	defer handlersMu.Unlock()
	if _, dup := handlers[eventID]; dup {
		panic("hooks: duplicate handler registration for " + eventID)
	}
	handlers[eventID] = h
}

// handlerFor is the registered handler for an event, or nil.
func handlerFor(eventID string) HookHandler {
	handlersMu.RLock()
	defer handlersMu.RUnlock()
	return handlers[eventID]
}

// RunHookEvent runs one hook event and returns what the host should see.
//
// An unrecognized event id is exit 2 with an error, not a panic: the id comes
// off a command line the host runs on every turn.
func RunHookEvent(ctx context.Context, eventID string, deps HookDeps) HookResult {
	if FindHookEvent(eventID) == nil {
		return HookResult{ExitCode: 2, Error: "unknown hook event: " + eventID}
	}
	if h := handlerFor(eventID); h != nil {
		return h(ctx, deps)
	}
	// An event with no behaviour yet still owes its host a well-formed answer:
	// silence under exit2, `{}` under a protocol that parses stdout every time.
	return renderNoOpFor(deps.Host)
}

// renderNoOpFor is `renderNoOp(protocolForHost(host))` from
// `src/hooks/protocol.mjs`.
//
// T045 owns protocol.go; when it lands, COLLAPSE this into that call. The single
// host-shape question is answered by hookCommandNeedsHostFlag (spec.go) so this
// file does not become a second table.
func renderNoOpFor(hostID string) HookResult {
	if hookCommandNeedsHostFlag(hostID) {
		// cursor-json: Cursor JSON.parse()s stdout unconditionally, so an empty
		// stdout is a parse error, not a silent allow. `{}` means "let it stop".
		return HookResult{ExitCode: 0, Stdout: "{}\n"}
	}
	return HookResult{ExitCode: 0}
}

// ParseEventPayload parses the harness's JSON event payload. An empty map if
// there is none or it does not parse — a malformed or absent payload is never an
// error in the agent turn, only an event with nothing this handler can act on.
//
// Takes the already-read input string the hook entrypoint hands every handler
// (ReadHookInput, #227) rather than reading stdin itself: stdin is read once,
// with one bounded deadline, and shared by every event.
func ParseEventPayload(input string) map[string]any {
	if input == "" {
		return map[string]any{}
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(input), &parsed); err != nil || parsed == nil {
		return map[string]any{}
	}
	return parsed
}

// shellToolNames matches the shell tool under every host's spelling.
var shellToolNames = regexp.MustCompile(`(?i)^(bash|shell|terminal|run_?command)$`)

// ShellCommandOf is the shell command a PreToolUse payload is about to run, or
// "" for any other tool.
//
// Accepts both the snake_case shape Claude Code and Codex send and the camelCase
// one Cursor's adapter uses (#228) — reading two key spellings costs nothing and
// being wrong on one host costs the whole feature there.
//
// Anything that is not a shell tool returns "", and nothing further happens: no
// policy read, no terminal, no token, no network. Lives here rather than in
// `pretooluse.go` because it lives in `run.mjs` in the original — T044 should
// call it, not re-implement it.
func ShellCommandOf(payload map[string]any) string {
	toolName := firstString(payload, "tool_name", "toolName")
	if !shellToolNames.MatchString(toolName) {
		return ""
	}
	input, _ := firstValue(payload, "tool_input", "toolInput").(map[string]any)
	if input == nil {
		return ""
	}
	command := firstString(input, "command", "cmd")
	if strings.TrimSpace(command) == "" {
		return ""
	}
	return command
}

func firstValue(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			return v
		}
	}
	return nil
}

// firstString is `payload?.a ?? payload?.b ?? ”` followed by JS's String().
func firstString(m map[string]any, keys ...string) string {
	v := firstValue(m, keys...)
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
