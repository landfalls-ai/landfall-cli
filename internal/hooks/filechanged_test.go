package hooks

// filechanged_test.go — the WAKE half of `test/hooks/file-changed.test.mjs`
// (#227). The doorbell, the stage and the digest already have their own ported
// suites (doorbell_test.go, stage_test.go, digest_test.go); this file is the
// hook that joins them.
//
// The gap under test is the one `stop` cannot cover: a session that is IDLE. No
// conclusion to block, no tool call to ride. It takes TWO hook events, because
// no single one can do it — `FileChanged` can watch the doorbell but the host
// discards its output, and `UserPromptSubmit` can speak to the model but never
// learns the room changed. So the wake STAGES and the prompt DELIVERS, and the
// cursor may only move at the second one.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestTheWakeConsumesNothingBecauseTheHostDiscardsItsOutput(t *testing.T) {
	// The defect the docs turned up: FileChanged "does not support decision
	// control. Exit code and JSON output are ignored." A hook that consumed here
	// would advance the cursor in exchange for a digest nobody reads, silently
	// swallowing room context — and taking it out of reach of #225's Stop hook
	// too.
	s := sessionWith(t, 3, 1)
	res := RunFileChangedHook(FileChangedOptions{
		Query:  staticQuery(peekOfSession(t, "/s", s)),
		Stage:  func([]SocketAnswer) bool { return true },
		Clear:  func() {},
		Notify: func(string) {},
	})

	if res.ExitCode != 0 || !res.Staged {
		t.Fatalf("got %+v", res)
	}
	if s.Cursor() != -1 {
		t.Fatalf("the cursor must not move at the wake, got %d", s.Cursor())
	}
	if len(s.Pending()) != 3 {
		t.Fatalf("the events must stay queued, got %d", len(s.Pending()))
	}
}

func TestTheWakeNudgesTheHumanOnStderr(t *testing.T) {
	var nudged string
	RunFileChangedHook(FileChangedOptions{
		Query:  staticQuery(peekOfSession(t, "/s", sessionWith(t, 2, 1))),
		Stage:  func([]SocketAnswer) bool { return true },
		Clear:  func() {},
		Notify: func(text string) { nudged = text },
	})
	if nudged != NudgeLine(2) {
		t.Fatalf("got %q", nudged)
	}
	if !strings.Contains(nudged, "handed to this session on your next message") {
		t.Fatalf("got %q", nudged)
	}
	if !strings.Contains(nudged, "2 update(s) from your war room") {
		t.Fatalf("got %q", nudged)
	}
}

func TestAStageThatCouldNotBeWrittenLeavesTheBellRinging(t *testing.T) {
	cleared := false
	RunFileChangedHook(FileChangedOptions{
		Query:  staticQuery(peekOfSession(t, "/s", sessionWith(t, 1, 1))),
		Stage:  func([]SocketAnswer) bool { return false },
		Clear:  func() { cleared = true },
		Notify: func(string) {},
	})
	if cleared {
		t.Fatal("one extra wake is recoverable; a lost nudge is not")
	}
}

func TestAWakeWithNothingOwedStagesNothingAndClearsNothing(t *testing.T) {
	res := RunFileChangedHook(FileChangedOptions{
		Query:  staticQuery(),
		Stage:  func([]SocketAnswer) bool { t.Fatal("nothing to stage"); return false },
		Clear:  func() { t.Fatal("a bell we cannot prove is stale must not be cleared") },
		Notify: func(string) { t.Fatal("an idle session must not be nudged for nothing") },
	})
	if res.ExitCode != 0 || res.Staged {
		t.Fatalf("got %+v", res)
	}
}

func TestAWakeThatCannotAskStaysSilentAndStillExitsZero(t *testing.T) {
	res := RunFileChangedHook(FileChangedOptions{
		Query:  func(SocketRequest) ([]SocketAnswer, error) { return nil, errors.New("permission denied") },
		Notify: func(string) { t.Fatal("a failed query must nudge nobody") },
	})
	if res.ExitCode != 0 {
		t.Fatalf("exit %d", res.ExitCode)
	}
}

func TestTheWakeStagesOnlyTheSessionsThatOweSomething(t *testing.T) {
	// Sessions owing nothing are dropped before the stage, so they never widen
	// it beyond what it is entitled to deliver.
	var staged []SocketAnswer
	RunFileChangedHook(FileChangedOptions{
		Query: staticQuery(
			owed("/owes", 1, 0, 3, 4, "#4 edge.finding [Dana] — origin pool unhealthy"),
			owed("/silent", 0, 0, 9, 9),
		),
		Stage:  func(peeks []SocketAnswer) bool { staged = peeks; return true },
		Clear:  func() {},
		Notify: func(string) {},
	})
	if len(staged) != 1 || staged[0].SocketPath != "/owes" {
		t.Fatalf("staged %+v", staged)
	}
}

func TestTheWakeWritesNothingToStdout(t *testing.T) {
	// Through the real dispatcher, so the handler's channel choice is what is
	// under test rather than a stand-in's.
	ws := tempWorkspace(t)
	var wroteStdout string
	got := RunHookEvent(context.Background(), "file-changed", HookDeps{
		Workspace: ws,
		Host:      "claude-code",
		Emit: func(text, channel string) {
			if channel == ChannelStdout {
				wroteStdout += text
			}
		},
	})
	if got.ExitCode != 0 {
		t.Fatalf("exit %d", got.ExitCode)
	}
	if wroteStdout != "" {
		t.Fatalf("nothing may be written to a channel the host ignores, got %q", wroteStdout)
	}
	if got.Stdout != "" {
		t.Fatalf("got %q", got.Stdout)
	}
}
