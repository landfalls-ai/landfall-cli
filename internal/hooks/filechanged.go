// filechanged.go — the doorbell wake (#227). A Go port of
// `src/hooks/file-changed.mjs`.
//
// This hook used to try to inject the digest itself. It cannot: Claude Code's
// `FileChanged` "does not support decision control. Exit code and JSON output
// are ignored." The event that can watch a file is not the event that can speak
// to the model.
//
// So the wake now does two things, neither of which needs the host to read our
// output:
//
//  1. STAGE the digest (stage.go) for `user-prompt-submit` to deliver.
//  2. NUDGE the human on stderr — the cheap side effect that reaches a person
//     watching the terminal even before the session resumes.
//
// And, critically, it consumes NOTHING. Advancing a cursor here would drop the
// events from the session's queue in exchange for a digest the host discards —
// silently swallowing room context, and taking it out of reach of #225's Stop
// hook too. `never consume without delivering` is the invariant; delivery
// happens in userpromptsubmit.go, and the cursor moves there.
package hooks

import (
	"context"
	"strconv"
)

func init() { RegisterHookHandler("file-changed", runFileChangedHandler) }

// NudgeLine is one line, plus a terminal bell, for a human who happens to be
// looking.
func NudgeLine(total int) string {
	return "⚡ landfall: " + strconv.Itoa(total) +
		" update(s) from your war room — they will be handed to this session on your next message."
}

// FileChangedOptions is everything RunFileChangedHook needs.
type FileChangedOptions struct {
	// Query asks every socket in the workspace what it is still owed.
	Query func(req SocketRequest) ([]SocketAnswer, error)
	// Stage parks the owed peeks for the next prompt, reporting whether it
	// landed.
	Stage func(peeks []SocketAnswer) bool
	// Clear answers the doorbell. Called ONLY after a successful stage.
	Clear func()
	// Notify writes the one nudge line for a human. The text arrives WITHOUT a
	// trailing newline, matching the Node original's `notify`.
	Notify func(text string)
}

// FileChangedOutcome is what one wake produces. Context is returned for tests
// and logging; nothing writes it anywhere.
type FileChangedOutcome struct {
	ExitCode int
	Staged   bool
	Context  string
}

// RunFileChangedHook runs the doorbell wake. Always exits 0 and never writes to
// stdout: the host ignores both, and a hook that logs onto a channel nobody
// parses is noise.
func RunFileChangedHook(opts FileChangedOptions) FileChangedOutcome {
	var peeks []SocketAnswer
	if opts.Query != nil {
		answers, err := opts.Query(PeekRequest())
		if err != nil {
			return FileChangedOutcome{ExitCode: 0, Staged: false, Context: ""}
		}
		peeks = answers
	}

	// The peeks that are actually owed something, PER SESSION — not the assembled
	// text. Delivery may need to speak for some of these sessions and not others
	// (see stage.go), and that cut can only be made while they are still
	// separate. Sessions owing nothing are dropped here so they never widen the
	// stage beyond what it is entitled to deliver, and the same filtered slice
	// then answers both remaining questions: is there anything to say, and how
	// much.
	owed := make([]SocketAnswer, 0, len(peeks))
	for _, p := range peeks {
		if OwesUpdates(p) {
			owed = append(owed, p)
		}
	}

	injection := BuildInjection(owed, 0)
	if !injection.Inject {
		// Some other file changed, or another session already took this context.
		// Nothing to stage, and nothing we can prove is a stale bell.
		return FileChangedOutcome{ExitCode: 0, Staged: false, Context: ""}
	}

	staged := false
	if opts.Stage != nil {
		staged = opts.Stage(owed)
	}

	total := 0
	for _, p := range owed {
		total += p.Response.CountOr(0) + p.Response.Dropped
	}
	if opts.Notify != nil {
		opts.Notify(NudgeLine(total))
	}

	// Clear the bell only once the digest is safely staged. If staging failed the
	// bell keeps ringing, which costs one extra wake and is the recoverable
	// direction — the events themselves are still queued on the socket either
	// way.
	if staged && opts.Clear != nil {
		opts.Clear()
	}

	return FileChangedOutcome{ExitCode: 0, Staged: staged, Context: injection.Context}
}

// runFileChangedHandler is the registered handler.
func runFileChangedHandler(_ context.Context, deps HookDeps) HookResult {
	ws := deps.Workspace
	out := RunFileChangedHook(FileChangedOptions{
		Query: workspaceQuery(ws),
		Stage: func(peeks []SocketAnswer) bool { return WriteStage(peeks, ws) },
		Clear: func() { ClearDoorbell(ws.Dir()) },
		// stderr, raw: this is the one channel that still reaches a person, and
		// the host discards everything else this event produces.
		Notify: func(text string) { deps.emit(text+"\n", ChannelStderr) },
	})
	result := "nothing-owed"
	switch {
	case out.Staged:
		result = "staged"
	case out.Context != "":
		// Something was owed and the stage would not write. The bell is left
		// ringing on purpose — one extra wake is recoverable.
		result = "stage-failed"
	}
	return HookResult{ExitCode: out.ExitCode, Result: result}
}
