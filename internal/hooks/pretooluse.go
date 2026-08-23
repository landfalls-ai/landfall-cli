// pretooluse.go — `pre-tool-use` (#233): match, ask, declare — in that order,
// never skipping the middle one. A Go port of `runPreToolUse` in
// `src/hooks/run.mjs`.
//
// EXIT 0, ALWAYS. In Claude Code a non-zero PreToolUse exit BLOCKS the tool
// call, and this hook exists to OBSERVE an investigation, not to gate one — an
// unreachable API, an expired session, a malformed policy file and a declined
// prompt are all "carry on". stdout stays empty because the host parses it;
// anything meant for the human goes to stderr through Log.
//
// THE ORDERING IS THE PRIVACY DESIGN, not an implementation detail. Each step is
// a gate that returns early, so the common case (a command matching nothing)
// touches only the policy file, and the rare case still cannot reach the network
// until a human has pressed `y`:
//
//	ShellCommandOf   not a shell tool → return. No policy read, no terminal, no
//	                 token, no network. Not even a file open.
//	LoadPolicy       no file, or no usable rules → return.
//	MatchCommand     no rule matches → return. The terminal is never touched.
//	ResolveTarget    resolved BEFORE the prompt: an intent that could not be sent
//	                 anyway is not worth interrupting anyone for.
//	Confirm          on /dev/tty. Every non-`y` path — including "no terminal at
//	                 all" — sends nothing.
//	SendIntent       only now, and only the rule's own literals.
package hooks

import (
	"context"
	"strings"
	"time"
)

func init() { RegisterHookHandler("pre-tool-use", runPreToolUseHandler) }

// PreToolUseOptions is everything RunPreToolUse needs, all injectable so the
// three guarantees this hook exists to keep can be asserted on directly:
// nothing leaves without the confirm, nothing unclassified leaves, and a
// non-matching command costs nothing.
type PreToolUseOptions struct {
	// Input is the host's payload — READ ONCE by the entrypoint.
	Input string
	// Log writes one human-readable line to stderr.
	Log func(line string)
	// ReadPolicy loads the local allow-list. Nil means LoadPolicy(PolicyPath()).
	ReadPolicy func() Policy
	// Confirm asks the engineer. Nil means ConfirmOnTTY.
	Confirm func(question string) bool
	// Resolve resolves where an intent would be sent. Nil means ResolveTarget.
	Resolve func() Target
	// Send posts the intent. Nil means SendIntent.
	Send func(target Target, intent Intent) DeclaredIntentOutcome
	// Now supplies the intent's startedAt. Nil means time.Now.
	Now func() time.Time
}

// PreToolUseOutcome is what one run produces. ExitCode is ALWAYS 0.
type PreToolUseOutcome struct {
	ExitCode int
	// Result is which gate stopped it (or that it went through), for tests and
	// logging. Never written to any stream.
	Result  string
	Outcome DeclaredIntentOutcome
}

// RunPreToolUse runs the pre-tool-use hook.
func RunPreToolUse(ctx context.Context, opts PreToolUseOptions) PreToolUseOutcome {
	log := opts.Log
	if log == nil {
		log = func(string) {}
	}

	command := ShellCommandOf(ParseEventPayload(opts.Input))
	if command == "" {
		return PreToolUseOutcome{ExitCode: 0, Result: "not-a-shell-command"}
	}

	readPolicy := opts.ReadPolicy
	if readPolicy == nil {
		readPolicy = func() Policy { return LoadPolicy(PolicyPath()) }
	}
	policy := readPolicy()
	// A policy file that cannot be understood is announced rather than ignored:
	// an engineer who believes they are covered and is not should hear about it
	// the first time a shell command runs, not never.
	for _, e := range policy.Errors {
		log("prod-policy: " + e)
	}
	if !policy.Exists || len(policy.Rules) == 0 {
		return PreToolUseOutcome{ExitCode: 0, Result: "no-policy"}
	}

	rule := MatchCommand(policy.Rules, command)
	if rule == nil {
		return PreToolUseOutcome{ExitCode: 0, Result: "no-match"}
	}

	// Resolved before the prompt: an intent that could not be sent anyway is not
	// worth interrupting anyone for.
	resolve := opts.Resolve
	if resolve == nil {
		resolve = func() Target { return ResolveTarget(ctx, TargetDeps{}) }
	}
	target := resolve()
	if !target.OK {
		log(`matched prod policy "` + rule.ID + `" but ` + target.Reason)
		return PreToolUseOutcome{ExitCode: 0, Result: "no-target"}
	}

	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	intent := IntentFor(*rule, now())

	confirm := opts.Confirm
	if confirm == nil {
		confirm = func(question string) bool { return ConfirmOnTTY(question, ConfirmOptions{}) }
	}
	if !confirm(ConfirmQuestion(*rule, intent)) {
		return PreToolUseOutcome{ExitCode: 0, Result: "declined"}
	}

	send := opts.Send
	if send == nil {
		send = func(t Target, i Intent) DeclaredIntentOutcome { return SendIntent(ctx, t, i, nil, 0) }
	}
	outcome := send(target, intent)
	log(DescribeOutcome(outcome))
	result := "declared"
	if outcome.Decision == "none" {
		result = "declared-none"
	}
	return PreToolUseOutcome{ExitCode: 0, Result: result, Outcome: outcome}
}

// ConfirmQuestion is the exact wording the engineer is asked.
//
// It names the RULE and the CLASSIFICATION and nothing else — no fragment of the
// command line appears in it, for the same reason none appears in the payload:
// the question is about what would be sent, and what would be sent is the rule.
func ConfirmQuestion(rule Rule, intent Intent) string {
	hints := ""
	if len(intent.EntityHints) > 0 {
		hints = " [" + strings.Join(intent.EntityHints, ", ") + "]"
	}
	return "landfall: this looks like a production investigation (" + rule.ID + "). " +
		`Report "` + intent.Category + `"` + hints + " " +
		"and open a war room for it?"
}

// runPreToolUseHandler is the registered handler. Every dependency is left nil,
// so the production path is the one described in the file header.
func runPreToolUseHandler(ctx context.Context, deps HookDeps) HookResult {
	out := RunPreToolUse(ctx, PreToolUseOptions{
		Input: deps.Input,
		Log:   func(line string) { deps.Logf("%s", line) },
		Now:   func() time.Time { return deps.Clock() },
	})
	// stdout stays empty on every path: the host parses it, and this hook has
	// nothing structured to say on any of them.
	return HookResult{ExitCode: out.ExitCode, Result: out.Result}
}
