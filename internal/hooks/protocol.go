// protocol.go — how a hook says "block" to the host that invoked it (#228,
// story #190). A Go port of `src/hooks/protocol.mjs`.
//
// The DECISION a lifecycle hook makes is host-independent: BuildStopDecision
// looks at what the room owes this session and returns block/allow plus the
// text. How that decision is DELIVERED is not — and the two hosts we shipped
// first happen to share a convention that Cursor does not:
//
//	EXIT2 (Claude Code, Codex)
//	  exit 2 with the message on stderr; the host feeds stderr back to the
//	  model. stdout is the host's own structured channel and must stay empty.
//
//	CURSOR_JSON (Cursor)
//	  exit 0 ALWAYS, with exactly one JSON object on stdout and nothing else.
//	  A non-zero exit is "the hook failed", not "the hook objected", and Cursor
//	  JSON.parse()s stdout — plain text or an empty stdout is a parse error, not
//	  a silent allow. So the allow path is `{}`, printed; it is not the absence
//	  of output.
//
// Cursor's `stop` is a NOTIFICATION hook: it cannot refuse a conclusion the way
// Claude Code's can. What it can do is `{"followup_message": "..."}`, which
// Cursor submits as the next user message and which continues the agent loop —
// the same effect our Stop refusal is after (the agent does not walk away from
// an incident holding stale context), reached by a different verb.
// `{continue:false}` — the shape issue #228 names — is the vocabulary of
// Cursor's PERMISSION hooks (`beforeShellExecution`, `beforeMCPExecution`) and
// of `beforeSubmitPrompt`, not of `stop`. Using it here would register a hook
// whose output Cursor ignores.
//
// TERMINATION under CURSOR_JSON rests on three independent things, because
// Cursor sends no `stop_hook_active`:
//  1. consume-after-block — whatever was reported is dequeued, so a second
//     followup needs genuinely new events (see stop.go);
//  2. `loop_count`, when Cursor sends it, read as the equivalent guard;
//  3. Cursor's own cap of 5 auto-followups per turn.
//
// (1) and (3) hold even if (2) is absent from a given Cursor version.
//
// THIS FILE IS THE SINGLE HOST TABLE. spec.go's HookCommand asks it whether a
// host needs `--host <id>` appended, and run.go's no-op answer asks it what
// silence looks like — neither keeps a second copy, because a registered
// command string and the handler that answers it drifting apart is a silent
// failure on somebody's machine, not a test failure here.
package hooks

import "encoding/json"

// EXIT2 is exit-2-with-stderr: Claude Code, Codex, and the default for anything
// unknown.
const EXIT2 = "exit2"

// CURSOR_JSON is one JSON object on stdout, exit 0: Cursor.
//
// Named in SCREAMING_SNAKE rather than Go's MixedCaps because it is the
// exported identifier `src/hooks/protocol.mjs` uses, and the ported tests
// assert on it by name — FR-001 parity, the same reasoning that keeps
// internal/instance's capitalized error wording.
//
//nolint:staticcheck // ST1003: deliberate, see above.
const CURSOR_JSON = "cursor-json"

// The two channels a verdict can be written on. A handler never picks one
// itself; the protocol does.
const (
	ChannelStdout = "stdout"
	ChannelStderr = "stderr"
)

var hostProtocol = map[string]string{
	"claude-code": EXIT2,
	"codex":       EXIT2,
	"cursor":      CURSOR_JSON,
}

// ProtocolForHost is the output protocol one host id speaks.
//
// An unknown id resolves to EXIT2 rather than failing: `--host` is read off a
// command line a host runs on every turn, and a hook that dies on an argument it
// does not recognize is worse than one that falls back to the convention two of
// the three hosts share.
func ProtocolForHost(hostID string) string {
	if p, ok := hostProtocol[hostID]; ok {
		return p
	}
	return EXIT2
}

// SpeaksOnSilence reports whether this host wants output even to say "nothing to
// report".
func SpeaksOnSilence(protocol string) bool { return protocol == CURSOR_JSON }

// Verdict is what one rendered decision looks like on the wire: the exit code,
// the text, and which channel it belongs on.
//
// An empty Text means "say nothing at all", which is the EXIT2 allow path and
// the only one where silence is a valid answer.
type Verdict struct {
	ExitCode int
	Text     string
	Channel  string
}

// cursorVerdict is the one JSON object CURSOR_JSON puts on stdout. `omitempty`
// is what makes the allow path render as exactly `{}`.
type cursorVerdict struct {
	FollowupMessage string `json:"followup_message,omitempty"`
}

// RenderStopVerdict renders a stop decision into what the invoking host
// understands.
func RenderStopVerdict(protocol string, block bool, reason string) Verdict {
	if protocol == CURSOR_JSON {
		// `{}` = "let it stop"; a followup_message = "here is what you missed,
		// keep going". Both are a successful run of the hook, hence exit 0.
		body := cursorVerdict{}
		if block && reason != "" {
			body.FollowupMessage = reason
		}
		// Marshal, never a hand-rolled concatenation: a multi-line reason must
		// come out as ONE line of stdout with the newlines escaped, or Cursor's
		// JSON.parse sees a truncated object.
		encoded, err := json.Marshal(body)
		if err != nil {
			// Unreachable for this struct; a well-formed allow is the safe answer
			// if it ever were not.
			encoded = []byte("{}")
		}
		return Verdict{ExitCode: 0, Text: string(encoded) + "\n", Channel: ChannelStdout}
	}
	if block && reason != "" {
		return Verdict{ExitCode: 2, Text: reason + "\n", Channel: ChannelStderr}
	}
	return Verdict{ExitCode: 0, Text: "", Channel: ChannelStderr}
}

// RenderNoOp is what a hook with no behaviour yet should print. Silence for
// EXIT2 (the documented "nothing pending, carry on"), `{}` for a host that
// parses stdout unconditionally.
func RenderNoOp(protocol string) Verdict {
	return RenderStopVerdict(protocol, false, "")
}
