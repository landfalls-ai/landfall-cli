// stop.go — the `stop` lifecycle hook (#225, story #190). A Go port of
// `src/hooks/stop.mjs`.
//
// An agent that has been investigating for ten minutes concludes with a summary
// it believes is current. Meanwhile another investigator published the finding
// that changes the answer. The war room saw it; this agent did not, because an
// MCP session only learns things in-band, on a tool result it chose to make.
//
// So: on Stop, ask every `landfall serve` session in this workspace whether room
// events newer than its cursor exist. If any do, spell them out and refuse the
// conclusion. If none do, exit silently and instantly.
//
// ORDER MATTERS — peek, block, THEN consume. `peek` is non-destructive, so if
// this process dies between reporting and consuming, the events stay queued and
// block again on the next attempt. That is the safe direction to fail: a
// duplicate nag costs a turn, a swallowed finding costs the incident.
//
// LOOP GUARD. Two independent things guarantee a session can always terminate:
//
//  1. `stop_hook_active` — the host sets it on the Stop that follows a block.
//     One acknowledgement is the whole ask; a second block on the same
//     conclusion would be the hook arguing with the agent.
//  2. Consume-after-block. Whatever was reported is removed from the queue, so a
//     further block requires genuinely NEW events. Without (1) an agent in a
//     very busy room could still be nagged repeatedly; without (2) it could
//     never stop at all. Both, so neither failure is reachable.
//
// #252 ADDS A SECOND REASON TO REFUSE, and it behaves differently: the room has
// QUARANTINED context this agent relied on, or a claim contradicting the
// admitted record is still awaiting its position. That is not discharged by
// reading — it stays true until the agent does something about it — so there is
// nothing to consume and guard (2) does not apply to it. Guard (1) does, and is
// what still guarantees termination: one refusal per conclusion, then the host's
// `stop_hook_active` lets the agent through. The alternative, refusing until the
// room's ruling is acted on, would be a hook that can strand a session on other
// people's votes.
//
// #228 ADDS A SECOND OUTPUT SHAPE and changes nothing above. The decision is
// still BuildStopDecision; only its delivery is host-parameterized, because
// Cursor reads a hook's verdict as JSON on stdout and cannot see an exit code or
// a line of stderr at all. See protocol.go — including why Cursor's termination
// guarantee does not rest on `stop_hook_active`, which it never sends.
package hooks

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/landfalls-ai/landfall-cli/internal/narrate"
)

// HookOutputMax is the ceiling on what the hook writes back. Hook output is
// folded into the model's context, and this is a nudge to go read the room — not
// a place to replay the timeline. The pointer at the end is how the full detail
// stays reachable.
const HookOutputMax = 10_000

func init() { RegisterHookHandler("stop", runStopHandler) }

// StopDecision is what BuildStopDecision produces: whether to block, what to
// say, and which sockets to advance afterwards.
type StopDecision struct {
	Block  bool
	Reason string
	// Consumes names each session's cursor move. Only EVENTS are consumed; a
	// quarantined citation is not made untrue by having been mentioned.
	Consumes []Consume
}

// BuildStopDecision turns `peek` answers into the decision. Pure — no I/O, no
// clock. A zero maxChars means HookOutputMax.
func BuildStopDecision(peeks []SocketAnswer, maxChars int) StopDecision {
	if maxChars <= 0 {
		maxChars = HookOutputMax
	}

	owed := make([]SocketAnswer, 0, len(peeks))
	for _, p := range peeks {
		if OwesUpdates(p) {
			owed = append(owed, p)
		}
	}
	total := 0
	for _, p := range owed {
		total += p.Response.CountOr(0) + p.Response.Dropped
	}

	// #252: the second, independent reason to refuse — the room has QUARANTINED
	// context this agent relied on, or a claim contradicting the admitted record
	// is still awaiting its position. Unlike unconsumed events this is not
	// discharged by reading: it is discharged by the agent doing something about
	// it, so nothing is consumed for it. The `stop_hook_active` guard in
	// RunStopHook is what still guarantees the session can terminate.
	blockerLines := narrate.DescribeStopBlockers(mergeBlockers(peeks), 0)

	if total == 0 && len(blockerLines) == 0 {
		return StopDecision{Block: false, Reason: "", Consumes: nil}
	}

	// Union across sessions (see QueryHookSockets): over-reporting a sibling
	// window's context is recoverable, under-reporting is the bug this prevents.
	var lines []string
	for _, p := range owed {
		lines = append(lines, p.Response.Digest...)
	}
	resumeSeq := int64(-1)
	for i, p := range owed {
		c := p.Response.CursorOr(-1)
		if i == 0 || c < resumeSeq {
			resumeSeq = c
		}
	}

	shown := lines
	if len(shown) > digestMaxLines {
		shown = shown[len(shown)-digestMaxLines:]
	}

	// Trim oldest-first until the whole message fits. The count of what was
	// dropped rises as lines leave, so the pointer stays truthful.
	body := shown
	reason := assembleStopReason(blockerLines, total, body, total-len(body), resumeSeq)
	for utf8.RuneCountInString(reason) > maxChars && len(body) > 1 {
		body = body[1:]
		reason = assembleStopReason(blockerLines, total, body, total-len(body), resumeSeq)
	}
	if utf8.RuneCountInString(reason) > maxChars {
		reason = string([]rune(reason)[:maxChars-1]) + "…"
	}

	// Consume everything each session owed, not just what was spelled out — the
	// pointer above is what makes the omitted detail re-fetchable, exactly as an
	// in-band flush on a tool result does.
	consumes := make([]Consume, 0, len(owed))
	for _, p := range owed {
		consumes = append(consumes, Consume{SocketPath: p.SocketPath, UpTo: p.Response.MaxSeqOr(-1)})
	}
	return StopDecision{Block: true, Reason: reason, Consumes: consumes}
}

// assembleStopReason is
// `[...blockerSection, ...eventSection(body, n)].filter(Boolean).join('\n')`.
//
// The blocker section goes FIRST when present: "you cited something the room has
// ruled wrong" outranks "there is unread news", and the two are separate asks
// with separate exits. The empty-string filter applies to the body lines too,
// not just the tail.
func assembleStopReason(blockerLines []string, total int, body []string, omitted int, resumeSeq int64) string {
	parts := make([]string, 0, len(blockerLines)+len(body)+5)
	if len(blockerLines) > 0 {
		parts = append(parts, narrate.BlockerHead)
		for _, l := range blockerLines {
			if l != "" {
				parts = append(parts, l)
			}
		}
		parts = append(parts, narrate.BlockerFoot)
	}
	if total > 0 {
		parts = append(parts, stopHead(total))
		for _, l := range body {
			if l != "" {
				parts = append(parts, l)
			}
		}
		if tail := stopTail(omitted, resumeSeq); tail != "" {
			parts = append(parts, tail)
		}
		parts = append(parts, stopFoot)
	}
	return strings.Join(parts, "\n")
}

func stopHead(total int) string {
	return "⚠ Do not conclude yet — " + strconv.Itoa(total) +
		" update(s) from other investigators reached this war room since you last read it:"
}

func stopTail(omitted int, resumeSeq int64) string {
	if omitted <= 0 {
		return ""
	}
	return "+" + strconv.Itoa(omitted) + " earlier update(s) not shown — call get_updates with sinceSeq=" +
		strconv.FormatInt(resumeSeq, 10) + " for the full detail."
}

const stopFoot = "Read these before you conclude. If they change your answer, say so and continue investigating; " +
	"if they do not, restate your conclusion and you will be allowed to stop."

// mergeBlockers returns blockers across every serve session in this workspace,
// de-duplicated.
//
// Two agent windows on one repo are two sessions with two cursors but often the
// same incident, so the same quarantine can arrive twice. Dedupe on the identity
// of the thing, not on the rendered line, so two sessions describing it slightly
// differently still collapse to one.
//
// THE IDENTITY IS (incident, seq), NOT seq. Sockets are joined by WORKSPACE —
// WorkspaceKey(cwd), with no incident in it — so two `landfall serve` processes
// in one checkout can be in two DIFFERENT incidents and both answer one `peek`.
// Sequence numbers are small per-incident counters, so a collision is ordinary
// rather than exotic, and a bare-seq dedupe silently dropped the losing session's
// real blocker on first-answer-wins. That inverts this file's own invariant:
// over-reporting is recoverable, under-reporting is the bug this exists to
// prevent.
//
// An answer with no incidentId (an older serve process, before the field was
// added to `peek`) falls back to its own socket path, so it never shares a scope
// with anything but itself: not with another old process, and not with a new
// one. During a partial upgrade — one old and one new serve process in the same
// incident — the same quarantined item is therefore reported twice rather than
// once. That is accepted, and deliberately not fixed by collapsing across scopes
// when the content matches: an old answer carries no incident, so matching it to
// a new one means guessing that equal sequence numbers mean the same item, which
// is precisely the assumption that produced the cross-incident drop above. A
// duplicate line is recoverable; a dropped blocker is the bug. The duplicate
// disappears once every serve process in the workspace is new.
func mergeBlockers(answers []SocketAnswer) narrate.Blockers {
	var out narrate.Blockers
	seenQuarantined := map[string]bool{}
	seenContradictions := map[string]bool{}
	for _, a := range answers {
		scope := a.Response.IncidentID
		if scope == "" {
			scope = "socket:" + socketPathOrPlaceholder(a.SocketPath)
		}
		b := narrate.StopBlockers(a.Response.Attention)
		for _, q := range b.Quarantined {
			key := blockerKey(scope, q.TargetSeq)
			if !seenQuarantined[key] {
				seenQuarantined[key] = true
				out.Quarantined = append(out.Quarantined, q)
			}
		}
		for _, c := range b.Contradictions {
			key := blockerKey(scope, c.ClaimSeq)
			if !seenContradictions[key] {
				seenContradictions[key] = true
				out.Contradictions = append(out.Contradictions, c)
			}
		}
	}
	return out
}

// socketPathOrPlaceholder is JS's `a?.socketPath ?? '?'`.
func socketPathOrPlaceholder(p string) string {
	if p == "" {
		return "?"
	}
	return p
}

// blockerKey is `JSON.stringify([scope, seq])`. A nil seq must key differently
// from seq 0 — the wire carries these as pointers precisely because absent and
// zero are different things.
func blockerKey(scope string, seq *int64) string {
	if seq == nil {
		return scope + "\x00null"
	}
	return scope + "\x00" + strconv.FormatInt(*seq, 10)
}

// stopPayload is the subset of a host's Stop payload this file reads. Everything
// is a pointer or an interface because ABSENT and FALSE are different answers on
// every one of these fields.
type stopPayload struct {
	StopHookActive  *bool    `json:"stop_hook_active"`
	StopHookActive2 *bool    `json:"stopHookActive"`
	LoopCount       *float64 `json:"loop_count"`
	Status          string   `json:"status"`
}

// parseStopPayload is `parseHookInput` — null for absent or unparseable input.
func parseStopPayload(input string) *stopPayload {
	if input == "" {
		return nil
	}
	var p stopPayload
	if err := json.Unmarshal([]byte(input), &p); err != nil {
		return nil
	}
	return &p
}

// IsStopHookActive reports whether the host is telling us this Stop already
// follows a block of ours.
//
// Claude Code and Codex both pass the hook a JSON object on stdin;
// `stop_hook_active` is the documented field. Absent or unparseable input is
// treated as a first attempt — the conservative reading, since the cost of being
// wrong is one extra nag rather than a silent conclusion.
func IsStopHookActive(input string) bool {
	p := parseStopPayload(input)
	if p == nil {
		return false
	}
	if p.StopHookActive != nil && *p.StopHookActive {
		return true
	}
	if p.StopHookActive2 != nil && *p.StopHookActive2 {
		return true
	}
	// Cursor sends no `stop_hook_active`. Where it reports how many auto-followups
	// this turn has already had, that count is the same guard under another name:
	// anything above zero means this Stop follows one of ours. Where it does not,
	// this is simply never true and termination rests on consume-after-block plus
	// Cursor's own cap of 5 followups (protocol.go).
	//
	// `Number.isFinite` does not coerce, so a STRING "1" is not the guard — which
	// is why LoopCount is a *float64 decoded from a JSON number and nothing else.
	return p.LoopCount != nil && *p.LoopCount >= 1
}

// IsConcludedTurn reports whether this Stop is a turn that actually CONCLUDED.
//
// Cursor's stop payload carries `status: 'completed' | 'aborted' | 'error'`. A
// turn the user interrupted, or one that died, is not an agent walking away from
// the room holding stale context — it is an agent that did not get to finish.
// Nagging it adds a followup message to a turn nobody is reading, and would
// consume the very events the next real conclusion needs to be told about. Hosts
// that send no status (Claude Code, Codex) are unaffected: absent means
// "concluded", the reading those hosts' Stop already implies.
func IsConcludedTurn(input string) bool {
	p := parseStopPayload(input)
	if p == nil {
		return true
	}
	return p.Status != "aborted" && p.Status != "error"
}

// StopOptions is everything RunStopHook needs, all injectable so the handler is
// testable without a serve process, a terminal or a war room.
type StopOptions struct {
	// Input is the host's payload — READ ONCE by the entrypoint.
	Input string
	// Protocol is EXIT2 or CURSOR_JSON.
	Protocol string
	// Query asks every socket in the workspace one question. An error means
	// "nothing known to be owed".
	Query func(req SocketRequest) ([]SocketAnswer, error)
	// Send moves one session's cursor.
	Send func(socketPath string, req SocketRequest) error
	// Emit writes the verdict on the channel the protocol names. Never called
	// with empty text.
	Emit func(text, channel string)
	// MaxChars overrides HookOutputMax. Zero means HookOutputMax.
	MaxChars int
}

// StopOutcome is what one stop run produces.
type StopOutcome struct {
	ExitCode int
	Blocked  bool
	Reason   string
}

// RunStopHook runs the stop hook.
//
// WHAT gets said is BuildStopDecision's; HOW is the protocol's (protocol.go) —
// exit 2 with the digest on stderr for Claude Code and Codex, one JSON object on
// stdout for Cursor. Under EXIT2 nothing is ever written to stdout, because the
// host parses that channel; under CURSOR_JSON stdout is exactly where the
// verdict goes and silence there is a parse error rather than an allow, so EVERY
// exit path below emits — including the ones that decline to block.
//
// Emit is called BEFORE the cursors move, so a crash mid-way leaves the events
// queued rather than consumed-but-never-delivered.
func RunStopHook(opts StopOptions) StopOutcome {
	protocol := opts.Protocol
	if protocol == "" {
		protocol = EXIT2
	}
	say := func(block bool, reason string) Verdict {
		v := RenderStopVerdict(protocol, block, reason)
		if v.Text != "" && opts.Emit != nil {
			opts.Emit(v.Text, v.Channel)
		}
		return v
	}
	allow := func() StopOutcome {
		return StopOutcome{ExitCode: say(false, "").ExitCode, Blocked: false, Reason: ""}
	}

	if IsStopHookActive(opts.Input) {
		return allow()
	}
	if !IsConcludedTurn(opts.Input) {
		return allow()
	}

	var peeks []SocketAnswer
	if opts.Query != nil {
		answers, err := opts.Query(PeekRequest())
		if err != nil {
			// A hook that fails must not become a hook that blocks. No serve
			// session, no socket, a permissions problem — all mean "nothing known
			// to be owed".
			return allow()
		}
		peeks = answers
	}

	decision := BuildStopDecision(peeks, opts.MaxChars)
	if !decision.Block {
		return allow()
	}

	exitCode := say(true, decision.Reason).ExitCode

	// A failed consume must never suppress the block that was already emitted —
	// the events staying queued is the recoverable outcome.
	if opts.Send != nil {
		for _, c := range decision.Consumes {
			_ = opts.Send(c.SocketPath, ConsumeRequest(c.UpTo))
		}
	}

	return StopOutcome{ExitCode: exitCode, Blocked: true, Reason: decision.Reason}
}

// runStopHandler is the registered handler: HookDeps in, the real socket
// functions wired up, a HookResult out.
func runStopHandler(_ context.Context, deps HookDeps) HookResult {
	out := RunStopHook(StopOptions{
		Input:    deps.Input,
		Protocol: ProtocolForHost(deps.Host),
		Query:    workspaceQuery(deps.Workspace),
		Send:     workspaceSend(),
		Emit:     deps.emit,
	})
	result := "allowed"
	if out.Blocked {
		result = "blocked"
	}
	// Stdout/Stderr stay EMPTY: the verdict has already been written, in the one
	// order that makes a crash mid-way recoverable.
	return HookResult{ExitCode: out.ExitCode, Result: result}
}

// workspaceQuery is the production Query: every socket in the workspace,
// concurrently. It never errors — QueryHookSockets already treats an unreachable
// socket as silence — so the error branch above exists for an injected query.
func workspaceQuery(ws Workspace) func(SocketRequest) ([]SocketAnswer, error) {
	return func(req SocketRequest) ([]SocketAnswer, error) {
		return QueryHookSockets(req, ws, 0), nil
	}
}

// workspaceSend is the production Send.
func workspaceSend() func(string, SocketRequest) error {
	return func(socketPath string, req SocketRequest) error {
		_, err := SendToSocket(socketPath, req, 0)
		return err
	}
}
