package hooks

// userpromptsubmit_test.go — the DELIVERY half of
// `test/hooks/file-changed.test.mjs` (#227), including the whole of
// `chooseDelivery`'s three-round bug history and the ring → wake → inject path
// end to end over real sockets, a real doorbell and a real stage.
//
// The rule under test, restated because it is the thing that keeps regressing:
// THE TWO SOURCES ARE UNIONED PER SESSION, NEVER RANKED. A session that answered
// its own peek speaks for itself (including "nothing owed", which retires its
// staged entry); a session that did not answer is spoken for by its staged
// entry, whether or not any OTHER session has live content this turn.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/session"
)

// stagedPeek is one staged session's peek answer, shaped the way socket.go
// replies.
func stagedPeek(t *testing.T, socketPath string, seq int64, text string) SocketAnswer {
	t.Helper()
	return peekAnswer(t, socketPath, PeekResponse{
		OK: true, V: SocketProtocolVersion, Count: 1, Dropped: 0,
		Cursor: seq - 1, MaxSeq: seq,
		Digest: []string{"#" + itoa(seq) + " edge.finding [x] — " + text},
	})
}

// silentPeek is a session that replied to the peek and has nothing left to hand
// over.
func silentPeek(t *testing.T, socketPath string, cursor int64) SocketAnswer {
	t.Helper()
	return peekAnswer(t, socketPath, PeekResponse{
		OK: true, V: SocketProtocolVersion, Count: 0, Dropped: 0,
		Cursor: cursor, MaxSeq: cursor, Digest: []string{},
	})
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func stageOf(peeks ...SocketAnswer) *Stage {
	return &Stage{V: StageVersion, Peeks: peeks}
}

func consumedPaths(d Delivery) []string {
	out := make([]string, 0, len(d.Consumes))
	for _, c := range d.Consumes {
		out = append(out, c.SocketPath)
	}
	return out
}

// --- chooseDelivery ----------------------------------------------------------

func TestALiveSocketDeliversAndTheStageCoversOneThatIsGone(t *testing.T) {
	staged := stageOf(stagedPeek(t, "/gone", 9, "staged"))

	live := ChooseDelivery([]SocketAnswer{stagedPeek(t, "/s", 4, "live")}, nil)
	if live.Source != "socket" {
		t.Fatalf("source %q", live.Source)
	}
	if !strings.Contains(live.Context, "#4") || !strings.Contains(live.Context, "live") {
		t.Fatalf("got:\n%s", live.Context)
	}

	// serve exited between the wake and the prompt: the socket is gone, but the
	// context should still arrive. That case is the only reason a stage exists.
	fromStage := ChooseDelivery(nil, staged)
	if fromStage.Source != "stage" || !strings.Contains(fromStage.Context, "#9") {
		t.Fatalf("got %+v", fromStage)
	}
	if ChooseDelivery(nil, nil).Inject {
		t.Fatal("nothing owed anywhere")
	}
	if ChooseDelivery(nil, &Stage{V: StageVersion}).Inject {
		t.Fatal("an empty stage is not a delivery")
	}
}

func TestASessionThatAnsweredRetiresItsOwnStagedEntryEvenWhenItOwesNothing(t *testing.T) {
	staged := stageOf(stagedPeek(t, "/a", 9, "a-only"))

	// The distinction the whole rule turns on. "Nothing owed" from a session that
	// REPLIED means the events reached the agent some other way — flushPending
	// rides every tool call — so the stage is spent, not pending.
	answered := ChooseDelivery([]SocketAnswer{silentPeek(t, "/a", 9)}, staged)
	if answered.Inject {
		t.Fatal("a spent stage must never re-inject")
	}
	if !answered.StaleStage {
		t.Fatal("and must be dropped, not left to surface later")
	}

	// Same stage, same silent live answer — but from a DIFFERENT session, so /a
	// itself was never reached.
	gone := ChooseDelivery([]SocketAnswer{silentPeek(t, "/someone-else", 4)}, staged)
	if gone.Source != "stage" || gone.StaleStage {
		t.Fatalf("got %+v", gone)
	}
}

func TestAPartiallyReachableStageDeliversOnlyTheSessionsThatNeverAnswered(t *testing.T) {
	// The residual half of the duplicate-delivery bug: /a answered (so
	// flushPending has already handed the agent #9 in-band) while /b's serve died
	// (so #3 has no other surviving copy). Answering the coarse question — "is
	// ANY owner unreachable?" — and then delivering the whole staged block
	// re-shows /a its own already-read event, captioned "while you were idle".
	both := stageOf(
		stagedPeek(t, "/a", 9, "already-read-by-a"),
		stagedPeek(t, "/b", 3, "only-copy-for-b"),
	)

	partial := ChooseDelivery([]SocketAnswer{silentPeek(t, "/a", 9)}, both)
	if partial.Source != "stage" {
		t.Fatalf("source %q", partial.Source)
	}
	if !strings.Contains(partial.Context, "only-copy-for-b") {
		t.Fatal("the orphaned session still gets its context")
	}
	if strings.Contains(partial.Context, "already-read-by-a") {
		t.Fatal("the answering session must not be re-told")
	}
	if !strings.HasPrefix(partial.Context, "⚡ 1 update(s)") {
		t.Fatalf("the count must be of what is actually delivered:\n%s", partial.Context)
	}
	if got := consumedPaths(partial); len(got) != 1 || got[0] != "/b" {
		t.Fatalf("no cursor may move on behalf of a session we are not delivering to: %v", got)
	}

	// Both gone: both portions are the only surviving copies, so both go.
	neither := ChooseDelivery(nil, both)
	if !strings.Contains(neither.Context, "already-read-by-a") || !strings.Contains(neither.Context, "only-copy-for-b") {
		t.Fatalf("got:\n%s", neither.Context)
	}

	// Both answered: nothing left for the stage to speak for.
	spent := ChooseDelivery([]SocketAnswer{silentPeek(t, "/a", 9), silentPeek(t, "/b", 3)}, both)
	if spent.Inject || !spent.StaleStage {
		t.Fatalf("got %+v", spent)
	}
}

func TestLiveContentNeverEclipsesAStagedSessionThatIsGone(t *testing.T) {
	// The third round of the same defect, in the opposite direction. Two windows
	// share one workspace, so one stage covers both. /b's serve dies; /a is still
	// live and still owes #9 of its own. Ranking the sources ("anyone owes
	// something live → deliver that, source: socket") returns before the stage is
	// ever read — and the caller then unstages unconditionally, DELETING /b's
	// only surviving copy undelivered.
	staged := stageOf(
		stagedPeek(t, "/a", 9, "also-owed-live-by-a"),
		stagedPeek(t, "/b", 3, "only-copy-for-b"),
	)
	out := ChooseDelivery([]SocketAnswer{stagedPeek(t, "/a", 9, "also-owed-live-by-a")}, staged)

	if !out.Inject {
		t.Fatal("expected a delivery")
	}
	if out.Source != "socket+stage" {
		t.Fatalf("both sources contributed, so neither may be named alone: %q", out.Source)
	}
	if !strings.Contains(out.Context, "only-copy-for-b") {
		t.Fatal("the dead session is delivered, not discarded behind a live one")
	}
	if !strings.Contains(out.Context, "also-owed-live-by-a") {
		t.Fatal("and the live session still gets its own")
	}
	if !strings.HasPrefix(out.Context, "⚡ 2 update(s)") {
		t.Fatalf("one digest, one honest total across both sources:\n%s", out.Context)
	}
	got := consumedPaths(out)
	if len(got) != 2 || !contains(got, "/a") || !contains(got, "/b") {
		t.Fatalf("every session folded into the block has its cursor advanced: %v", got)
	}
	if out.StaleStage {
		t.Fatal("the stage contributed, so it is not stale")
	}
	// …and /a's staged entry is not delivered TWICE for being in both sources.
	if n := strings.Count(out.Context, "also-owed-live-by-a"); n != 1 {
		t.Fatalf("delivered %d times", n)
	}
}

func TestAStageWhoseOrphanedSessionsOweNothingIsSpentNotDeliveredEmpty(t *testing.T) {
	// An orphan can be present and still have nothing to say — a stage written
	// for a session that was then consumed by another path before dying. It must
	// be dropped rather than injected as a zero-update block.
	out := ChooseDelivery(nil, stageOf(silentPeek(t, "/b", 4)))
	if out.Inject || !out.StaleStage {
		t.Fatalf("got %+v", out)
	}
	if out.Source != "none" {
		t.Fatalf("source %q", out.Source)
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// --- the payload -------------------------------------------------------------

func TestPromptPayloadIsClaudeCodesStructuredOutputForThisEvent(t *testing.T) {
	body, err := json.Marshal(PromptPayload("shared context"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.HookSpecificOutput.HookEventName != "UserPromptSubmit" {
		t.Fatalf("got %q", parsed.HookSpecificOutput.HookEventName)
	}
	if parsed.HookSpecificOutput.AdditionalContext != "shared context" {
		t.Fatalf("got %q", parsed.HookSpecificOutput.AdditionalContext)
	}
}

// --- the handler -------------------------------------------------------------

func TestThePromptEmitsBeforeAnyCursorMovesThenUnstages(t *testing.T) {
	var order []string
	res := RunUserPromptSubmitHook(UserPromptSubmitOptions{
		Query:   staticQuery(peekOfSession(t, "/s", sessionWith(t, 1, 1))),
		Send:    func(_ string, req SocketRequest) error { order = append(order, "consume:1"); _ = req; return nil },
		Stage:   func() *Stage { return nil },
		Unstage: func() { order = append(order, "unstage") },
		Clear:   func() { order = append(order, "clear") },
		Emit:    func(string) { order = append(order, "emit") },
	})
	if !res.Injected {
		t.Fatal("expected a delivery")
	}
	want := "emit,consume:1,unstage,clear"
	if strings.Join(order, ",") != want {
		t.Fatalf("got %v, want %s", order, want)
	}
}

func TestThePromptNeverBlocksTheHumanEvenWithARoomFullOfContext(t *testing.T) {
	// This event CAN block a prompt. Refusing someone's message to show them a
	// digest would be a worse interruption than the one this feature prevents.
	res := RunUserPromptSubmitHook(UserPromptSubmitOptions{
		Query:   staticQuery(peekOfSession(t, "/s", sessionWith(t, 50, 1))),
		Send:    func(string, SocketRequest) error { return nil },
		Stage:   func() *Stage { return nil },
		Unstage: func() {},
		Clear:   func() {},
		Emit:    func(string) {},
	})
	if res.ExitCode != 0 {
		t.Fatalf("exit %d", res.ExitCode)
	}
}

func TestAPromptWithNothingOwedAndNothingStagedSaysNothingAtAll(t *testing.T) {
	res := RunUserPromptSubmitHook(UserPromptSubmitOptions{
		Query:   staticQuery(),
		Stage:   func() *Stage { return nil },
		Emit:    func(string) { t.Fatal("every prompt runs this hook — silence is the common case") },
		Unstage: func() { t.Fatal("nothing to unstage") },
	})
	if res.ExitCode != 0 || res.Injected {
		t.Fatalf("got %+v", res)
	}
}

func TestTheStageIsDroppedEvenWhenItsSocketsAreGoneSoItCannotReInject(t *testing.T) {
	unstaged := false
	res := RunUserPromptSubmitHook(UserPromptSubmitOptions{
		Query:   staticQuery(),
		Stage:   func() *Stage { return stageOf(stagedPeek(t, "/gone", 3, "staged digest")) },
		Send:    func(string, SocketRequest) error { return errors.New("ECONNREFUSED") },
		Unstage: func() { unstaged = true },
		Clear:   func() {},
		Emit:    func(string) {},
	})
	if !res.Injected || res.Source != "stage" {
		t.Fatalf("got %+v", res)
	}
	if !unstaged {
		t.Fatal("a delivered stage must never be delivered twice")
	}
}

func TestThePromptNeverUnstagesASessionItDidNotDeliver(t *testing.T) {
	// The end-to-end shape of the union rule: window A is live and owes #9,
	// window B's serve died holding #3, and one stage covers both. The block that
	// goes out must contain B's event BEFORE unstage destroys the only copy of it.
	var emitted string
	var unstagedAfter string
	res := RunUserPromptSubmitHook(UserPromptSubmitOptions{
		Query: staticQuery(stagedPeek(t, "/a", 9, "live-for-a")),
		Stage: func() *Stage {
			return stageOf(stagedPeek(t, "/a", 9, "live-for-a"), stagedPeek(t, "/b", 3, "only-copy-for-b"))
		},
		Send: func(socketPath string, _ SocketRequest) error {
			if socketPath == "/b" {
				return errors.New("ECONNREFUSED")
			}
			return nil
		},
		Unstage: func() { unstagedAfter = emitted },
		Clear:   func() {},
		Emit:    func(text string) { emitted = text },
	})

	if !res.Injected || res.Source != "socket+stage" {
		t.Fatalf("got %+v", res)
	}
	if !strings.Contains(res.Context, "only-copy-for-b") {
		t.Fatal("B was delivered, not deleted behind A")
	}
	if !strings.Contains(res.Context, "live-for-a") {
		t.Fatal("A still gets its own")
	}
	if !strings.Contains(unstagedAfter, "only-copy-for-b") {
		t.Fatal("the stage went only after that block was emitted")
	}
}

func TestAPartialConsumeFailureLeavesTheBellRingingForTheSessionItMissed(t *testing.T) {
	cleared := false
	res := RunUserPromptSubmitHook(UserPromptSubmitOptions{
		Query: staticQuery(
			peekOfSession(t, "/a", sessionWith(t, 2, 1)),
			peekOfSession(t, "/b", sessionWith(t, 1, 9)),
		),
		Send: func(socketPath string, _ SocketRequest) error {
			if socketPath == "/b" {
				return errors.New("timed out after 250ms")
			}
			return nil
		},
		Stage:   func() *Stage { return nil },
		Unstage: func() {},
		Clear:   func() { cleared = true },
		Emit:    func(string) {},
	})
	if !res.Injected {
		t.Fatal("the digest still went out — the failure is downstream of delivery")
	}
	if cleared {
		t.Fatal("the bell must keep ringing for the session the consume missed")
	}
}

func TestRunHookEventRoutesBothHalvesToTheirHandlers(t *testing.T) {
	ws := tempWorkspace(t)
	var stdoutText string
	got := RunHookEvent(context.Background(), "user-prompt-submit", HookDeps{
		Workspace: ws,
		Host:      "claude-code",
		Emit: func(text, channel string) {
			if channel == ChannelStdout {
				stdoutText += text
			}
		},
	})
	if got.ExitCode != 0 {
		t.Fatalf("exit %d", got.ExitCode)
	}
	// Nothing owed in an empty workspace: this event fires on EVERY prompt, so
	// the common case must cost nothing and say nothing.
	if stdoutText != "" {
		t.Fatalf("got %q", stdoutText)
	}
}

// --- ring → wake → inject, over real sockets ---------------------------------

// consumeUpTo is the production ConsumeFunc a serve process passes.
func consumeUpTo(s SocketSession, upTo int64) int64 {
	return s.(*session.Session).ConsumeUpTo(upTo)
}

func TestTheWholeIdlePathRingWakeStageResumeDeliverConsumeClear(t *testing.T) {
	ws := tempWorkspace(t)
	s := session.New(session.Options{Client: client.New(client.Config{IncidentID: "inc-1", Slug: "acme"}, nil)})
	s.SetAttention(&client.Attention{})
	s.SetAttentionDirty(false)
	bound := StartHookSocket(context.Background(), s, StartOptions{Workspace: ws, PID: 3001, Consume: consumeUpTo})
	if bound == nil {
		t.Fatal("expected the socket to bind")
	}
	defer func() { _ = bound.Close() }()
	bell := NewDoorbell(DoorbellOptions{Cwd: ws.Dir()})

	// 1. serve parks a pushed event and rings, because pending went 0 → 1.
	seven := int64(7)
	s.EnqueueEvent(context.Background(), client.Event{
		Seq: &seven, Type: "edge.finding",
		Payload: map[string]any{"displayName": "Dana", "text": "origin pool unhealthy"},
	})
	bell.Ring(len(s.Pending()))
	if info, err := os.Stat(DoorbellPath(ws.Dir())); err != nil || info.Size() == 0 {
		t.Fatalf("the bell did not ring: %v", err)
	}

	// 2. the file watcher fires. The wake stages and nudges — and MUST NOT
	//    consume, because the host throws this hook's output away.
	var nudged string
	wake := RunFileChangedHook(FileChangedOptions{
		Query:  workspaceQuery(ws),
		Stage:  func(peeks []SocketAnswer) bool { return WriteStage(peeks, ws) },
		Clear:  func() { ClearDoorbell(ws.Dir()) },
		Notify: func(text string) { nudged = text },
	})
	if !wake.Staged {
		t.Fatal("the wake must stage")
	}
	if !strings.Contains(nudged, "1 update(s) from your war room") {
		t.Fatalf("got %q", nudged)
	}
	if s.Cursor() != -1 || len(s.Pending()) != 1 {
		t.Fatalf("the wake must not move the cursor: cursor=%d pending=%d", s.Cursor(), len(s.Pending()))
	}
	if info, err := os.Stat(DoorbellPath(ws.Dir())); err != nil || info.Size() != 0 {
		t.Fatalf("the bell is answered once staged: %v", err)
	}
	staged := ReadStage(ws)
	if staged == nil || !strings.Contains(BuildInjection(staged.Peeks, 0).Context, "#7 edge.finding [Dana] — origin pool unhealthy") {
		t.Fatalf("staged %+v", staged)
	}

	// 3. the human sends their next message. NOW it is delivered, and only now
	//    may the cursor move.
	var emitted string
	delivered := RunUserPromptSubmitHook(UserPromptSubmitOptions{
		Query:   workspaceQuery(ws),
		Send:    workspaceSend(),
		Stage:   func() *Stage { return ReadStage(ws) },
		Unstage: func() { ClearStage(ws) },
		Clear:   func() { ClearDoorbell(ws.Dir()) },
		Emit:    func(text string) { emitted = text },
	})
	if delivered.Source != "socket" {
		t.Fatalf("a live socket speaks for itself: %q", delivered.Source)
	}
	var payload PromptOutput
	if err := json.Unmarshal([]byte(emitted), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.HookSpecificOutput.HookEventName != "UserPromptSubmit" {
		t.Fatalf("got %q", payload.HookSpecificOutput.HookEventName)
	}
	if !strings.Contains(payload.HookSpecificOutput.AdditionalContext, "#7 edge.finding [Dana]") {
		t.Fatalf("got %q", payload.HookSpecificOutput.AdditionalContext)
	}
	if s.Cursor() != 7 || len(s.Pending()) != 0 {
		t.Fatalf("cursor=%d pending=%d", s.Cursor(), len(s.Pending()))
	}
	if ReadStage(ws) != nil {
		t.Fatal("a delivered stage is dropped")
	}

	// 4. the next prompt on an unchanged room is silent.
	again := RunUserPromptSubmitHook(UserPromptSubmitOptions{
		Query:   workspaceQuery(ws),
		Send:    workspaceSend(),
		Stage:   func() *Stage { return ReadStage(ws) },
		Unstage: func() { ClearStage(ws) },
		Clear:   func() { ClearDoorbell(ws.Dir()) },
		Emit:    func(string) { t.Fatal("nothing left to deliver") },
	})
	if again.Injected {
		t.Fatal("nothing left to deliver")
	}
}

func TestServeExitsBetweenTheWakeAndThePromptSoTheStageStillDelivers(t *testing.T) {
	ws := tempWorkspace(t)
	s := sessionWith(t, 2, 1)
	s.SetAttention(&client.Attention{})
	s.SetAttentionDirty(false)
	bound := StartHookSocket(context.Background(), s, StartOptions{Workspace: ws, PID: 3002, Consume: consumeUpTo})
	if bound == nil {
		t.Fatal("expected the socket to bind")
	}
	NewDoorbell(DoorbellOptions{Cwd: ws.Dir()}).Ring(2)
	RunFileChangedHook(FileChangedOptions{
		Query:  workspaceQuery(ws),
		Stage:  func(peeks []SocketAnswer) bool { return WriteStage(peeks, ws) },
		Clear:  func() { ClearDoorbell(ws.Dir()) },
		Notify: func(string) {},
	})

	// The whole reason a stage exists rather than having the prompt re-query.
	_ = bound.Close()

	var emitted string
	delivered := RunUserPromptSubmitHook(UserPromptSubmitOptions{
		Query:   workspaceQuery(ws),
		Send:    workspaceSend(),
		Stage:   func() *Stage { return ReadStage(ws) },
		Unstage: func() { ClearStage(ws) },
		Clear:   func() { ClearDoorbell(ws.Dir()) },
		Emit:    func(text string) { emitted = text },
	})
	if !delivered.Injected || delivered.Source != "stage" {
		t.Fatalf("got %+v", delivered)
	}
	var payload PromptOutput
	if err := json.Unmarshal([]byte(emitted), &payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload.HookSpecificOutput.AdditionalContext, "#1 edge.finding") {
		t.Fatalf("got %q", payload.HookSpecificOutput.AdditionalContext)
	}
	if ReadStage(ws) != nil {
		t.Fatal("a delivered stage is dropped")
	}
}

func TestASpentStageIsDroppedBeforeServeCanExitAndMakeItLookPendingAgain(t *testing.T) {
	// Without the drop, this is how the duplicate finally lands: the stage
	// outlives the session that could have contradicted it.
	ws := tempWorkspace(t)
	s := session.New(session.Options{Client: client.New(client.Config{IncidentID: "inc-1", Slug: "acme"}, nil)})
	s.SetAttention(&client.Attention{})
	s.SetAttentionDirty(false)
	bound := StartHookSocket(context.Background(), s, StartOptions{Workspace: ws, PID: 3202, Consume: consumeUpTo})
	if bound == nil {
		t.Fatal("expected the socket to bind")
	}

	// A stage naming this very session, which owes nothing.
	if !WriteStage([]SocketAnswer{stagedPeek(t, bound.SocketPath, 4, "stale digest")}, ws) {
		t.Fatal("WriteStage failed")
	}
	injected := false
	RunUserPromptSubmitHook(UserPromptSubmitOptions{
		Query:   workspaceQuery(ws),
		Send:    workspaceSend(),
		Stage:   func() *Stage { return ReadStage(ws) },
		Unstage: func() { ClearStage(ws) },
		Clear:   func() { ClearDoorbell(ws.Dir()) },
		Emit:    func(string) { injected = true },
	})
	_ = bound.Close()

	if injected {
		t.Fatal("re-delivering context the agent already read breaks nothing-arrives-twice")
	}
	if ReadStage(ws) != nil {
		t.Fatal("the spent stage is dropped, not left to fire once serve exits")
	}

	// serve is gone now; with the stage already dropped there is nothing left to
	// resurrect.
	after := RunUserPromptSubmitHook(UserPromptSubmitOptions{
		Query:   workspaceQuery(ws),
		Send:    workspaceSend(),
		Stage:   func() *Stage { return ReadStage(ws) },
		Unstage: func() { ClearStage(ws) },
		Clear:   func() { ClearDoorbell(ws.Dir()) },
		Emit:    func(string) { t.Fatal("a dropped stage cannot come back") },
	})
	if after.Injected {
		t.Fatal("a dropped stage cannot come back")
	}
}

func TestTwoIdleSessionsInOneWorkspaceEachKeepTheirOwnCursor(t *testing.T) {
	// The old spool design's failure: whoever consumed first truncated the file
	// and the other session never saw those events. Content lives on per-session
	// sockets now, so one wake serves both correctly.
	ws := tempWorkspace(t)
	a := sessionWith(t, 2, 1)
	b := sessionWith(t, 1, 9)
	for _, s := range []*session.Session{a, b} {
		s.SetAttention(&client.Attention{})
		s.SetAttentionDirty(false)
	}
	boundA := StartHookSocket(context.Background(), a, StartOptions{Workspace: ws, PID: 3101, Consume: consumeUpTo})
	boundB := StartHookSocket(context.Background(), b, StartOptions{Workspace: ws, PID: 3102, Consume: consumeUpTo})
	if boundA == nil || boundB == nil {
		t.Fatal("expected both sockets to bind")
	}
	defer func() { _ = boundA.Close(); _ = boundB.Close() }()

	var emitted string
	RunUserPromptSubmitHook(UserPromptSubmitOptions{
		Query:   workspaceQuery(ws),
		Send:    workspaceSend(),
		Stage:   func() *Stage { return ReadStage(ws) },
		Unstage: func() { ClearStage(ws) },
		Clear:   func() { ClearDoorbell(ws.Dir()) },
		Emit:    func(text string) { emitted = text },
	})
	var payload PromptOutput
	if err := json.Unmarshal([]byte(emitted), &payload); err != nil {
		t.Fatal(err)
	}
	ctx := payload.HookSpecificOutput.AdditionalContext
	for _, want := range []string{"3 update(s)", "#1 edge.finding", "#9 edge.finding"} {
		if !strings.Contains(ctx, want) {
			t.Fatalf("missing %q:\n%s", want, ctx)
		}
	}
	if a.Cursor() != 2 || b.Cursor() != 9 {
		t.Fatalf("cursors: a=%d b=%d", a.Cursor(), b.Cursor())
	}
}

func TestTheSocketIsTheOnlyTransportTheMarkerNeverCarriesAnEvent(t *testing.T) {
	ws := tempWorkspace(t)
	s := sessionWith(t, 3, 1)
	s.SetAttention(&client.Attention{})
	s.SetAttentionDirty(false)
	bound := StartHookSocket(context.Background(), s, StartOptions{Workspace: ws, PID: 3201, Consume: consumeUpTo})
	if bound == nil {
		t.Fatal("expected the socket to bind")
	}
	defer func() { _ = bound.Close() }()

	NewDoorbell(DoorbellOptions{Cwd: ws.Dir()}).Ring(len(s.Pending()))
	marker, err := os.ReadFile(DoorbellPath(ws.Dir()))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"origin 5xx", "Ana", "edge.finding", "inc-1", "acme"} {
		if strings.Contains(string(marker), secret) {
			t.Fatalf("marker leaked %q", secret)
		}
	}
	// …and yet the hook can still render every one of them, from the socket.
	peeks := QueryHookSockets(PeekRequest(), ws, 0)
	if !strings.Contains(BuildInjection(peeks, 0).Context, "origin 5xx spiking") {
		t.Fatalf("got:\n%s", BuildInjection(peeks, 0).Context)
	}
}
