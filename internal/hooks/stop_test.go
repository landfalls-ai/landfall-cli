package hooks

// stop_test.go — the DECISION and HANDLER layers of
// `test/hooks/stop-hook.test.mjs` (#225), plus layer 2 of
// `test/hooks/cursor-adapter.test.mjs` (#228).
//
// The protocol layer of stop-hook.test.mjs (handleSocketRequest against a real
// session) already lives in socket_test.go, and the wire+CLI layer of both
// suites lives in internal/cli/hookevents_test.go — this file is everything in
// between, which is where the ordering and termination guarantees are.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/narrate"
	"github.com/landfalls-ai/landfall-cli/internal/session"
)

// peekOfSession is `handleSocketRequest({op:'peek'}, session)` tagged with a
// socket path — the same shape a real query returns.
func peekOfSession(t *testing.T, socketPath string, s SocketSession) SocketAnswer {
	t.Helper()
	res, ok := HandleSocketRequest(PeekRequest(), s, HandleOptions{PID: 1}).(PeekResponse)
	if !ok {
		t.Fatalf("peek did not answer a PeekResponse")
	}
	return peekAnswer(t, socketPath, res)
}

// sessionWith is a session with n queued room events, as watchIncident would
// have parked them.
func sessionWith(t *testing.T, n int, from int64) *session.Session {
	t.Helper()
	s := session.New(session.Options{Client: client.New(client.Config{IncidentID: "inc-1", Slug: "acme"}, nil)})
	for i := 0; i < n; i++ {
		n := from + int64(i)
		s.EnqueueEvent(context.Background(), client.Event{
			Seq:     &n,
			Type:    "edge.finding",
			Payload: map[string]any{"displayName": "Ana", "text": fmt.Sprintf("origin 5xx spiking on shard %d", i)},
		})
	}
	return s
}

// --- 1. the decision ---------------------------------------------------------

func TestNothingOwedMeansNoBlockAndNothingToSay(t *testing.T) {
	got := BuildStopDecision(nil, 0)
	if got.Block || got.Reason != "" || len(got.Consumes) != 0 {
		t.Fatalf("got %+v", got)
	}
	if BuildStopDecision([]SocketAnswer{owed("/s", 0, 0, 7, 7)}, 0).Block {
		t.Fatal("a session that owes nothing must not block")
	}
}

func TestEventsOwedMeansBlockWithTheDigestAndAResumePointer(t *testing.T) {
	decision := BuildStopDecision([]SocketAnswer{peekOfSession(t, "/s", sessionWith(t, 2, 1))}, 0)

	if !decision.Block {
		t.Fatal("expected a block")
	}
	for _, want := range []string{
		"Do not conclude yet — 2 update(s)",
		"#1 edge.finding [Ana]",
		"#2 edge.finding [Ana]",
	} {
		if !strings.Contains(decision.Reason, want) {
			t.Fatalf("reason is missing %q:\n%s", want, decision.Reason)
		}
	}
	if len(decision.Consumes) != 1 || decision.Consumes[0] != (Consume{SocketPath: "/s", UpTo: 2}) {
		t.Fatalf("got %+v", decision.Consumes)
	}
}

func TestABusyRoomIsSummarizedNotReplayed(t *testing.T) {
	decision := BuildStopDecision([]SocketAnswer{peekOfSession(t, "/s", sessionWith(t, 50, 1))}, 0)

	if !decision.Block {
		t.Fatal("expected a block")
	}
	if n := utf8.RuneCountInString(decision.Reason); n > HookOutputMax {
		t.Fatalf("reason was %d chars", n)
	}
	if !strings.Contains(decision.Reason, "earlier update(s) not shown — call get_updates with sinceSeq=-1") {
		t.Fatalf("no truthful resume pointer:\n%s", decision.Reason)
	}
	// Every line the digest omits is still counted in the header total.
	if !strings.Contains(decision.Reason, "50 update(s)") {
		t.Fatalf("header total is wrong:\n%s", decision.Reason)
	}
}

func TestASingleEnormousEventStillFitsTheCap(t *testing.T) {
	s := session.New(session.Options{Client: client.New(client.Config{}, nil)})
	nine := int64(9)
	s.EnqueueEvent(context.Background(), client.Event{
		Seq:     &nine,
		Type:    "edge.finding",
		Payload: map[string]any{"text": strings.Repeat("x", 40_000)},
	})
	decision := BuildStopDecision([]SocketAnswer{peekOfSession(t, "/s", s)}, 0)
	if n := utf8.RuneCountInString(decision.Reason); n > HookOutputMax {
		t.Fatalf("reason was %d chars", n)
	}
}

func TestOverflowTheSessionAlreadyDroppedIsStillReported(t *testing.T) {
	// PENDING_MAX is 50, so 60 enqueued events leave 10 dropped.
	answer := peekOfSession(t, "/s", sessionWith(t, 60, 1))
	if answer.Response.Dropped == 0 {
		t.Fatal("expected the session to have dropped something")
	}
	total := answer.Response.CountOr(0) + answer.Response.Dropped
	decision := BuildStopDecision([]SocketAnswer{answer}, 0)
	if !strings.Contains(decision.Reason, fmt.Sprintf("%d update(s)", total)) {
		t.Fatalf("want a total of %d:\n%s", total, decision.Reason)
	}
}

func TestTwoLocalSessionsAreUnionedOverReportingNeverUnderReporting(t *testing.T) {
	decision := BuildStopDecision([]SocketAnswer{
		peekOfSession(t, "/a", sessionWith(t, 2, 1)),
		peekOfSession(t, "/b", sessionWith(t, 1, 9)),
	}, 0)

	if !strings.Contains(decision.Reason, "3 update(s)") {
		t.Fatalf("got:\n%s", decision.Reason)
	}
	want := []Consume{{SocketPath: "/a", UpTo: 2}, {SocketPath: "/b", UpTo: 9}}
	if len(decision.Consumes) != 2 || decision.Consumes[0] != want[0] || decision.Consumes[1] != want[1] {
		t.Fatalf("got %+v", decision.Consumes)
	}
}

// --- 2. #252's second reason to refuse ---------------------------------------

// quarantinePeek is a peek answer carrying an attention projection with one
// quarantined item, in a named incident.
func quarantinePeek(t *testing.T, socketPath, incidentID string, targetSeq int64) SocketAnswer {
	t.Helper()
	return peekAnswer(t, socketPath, PeekResponse{
		OK: true, V: SocketProtocolVersion, IncidentID: incidentID,
		Count: 0, Cursor: 4, MaxSeq: 4, Digest: []string{},
		Attention: &client.Attention{FlaggedOwnContext: []client.FlaggedContext{{
			TargetSeq: &targetSeq, TargetKind: "finding", State: "quarantined",
			Relation: "cited", Reason: "the graph was mislabelled",
		}}},
	})
}

func TestAQuarantinedCitationBlocksWithNothingToConsume(t *testing.T) {
	decision := BuildStopDecision([]SocketAnswer{quarantinePeek(t, "/s", "inc-1", 12)}, 0)
	if !decision.Block {
		t.Fatal("a quarantined citation must stop a conclusion")
	}
	if !strings.Contains(decision.Reason, narrate.BlockerHead) {
		t.Fatalf("got:\n%s", decision.Reason)
	}
	if !strings.Contains(decision.Reason, "seq 12 — the room QUARANTINED this finding you cited") {
		t.Fatalf("got:\n%s", decision.Reason)
	}
	// Unlike unconsumed events this is not discharged by reading, so nothing is
	// consumed for it.
	if len(decision.Consumes) != 0 {
		t.Fatalf("consumes %+v — a quarantined citation is not made untrue by having been mentioned", decision.Consumes)
	}
}

func TestTheBlockerSectionComesBeforeTheEventSection(t *testing.T) {
	// "You cited something the room has ruled wrong" outranks "there is unread
	// news", and the two are separate asks with separate exits.
	events := peekOfSession(t, "/a", sessionWith(t, 1, 1))
	decision := BuildStopDecision([]SocketAnswer{quarantinePeek(t, "/b", "inc-1", 12), events}, 0)

	blockerAt := strings.Index(decision.Reason, narrate.BlockerHead)
	eventsAt := strings.Index(decision.Reason, "update(s) from other investigators")
	if blockerAt < 0 || eventsAt < 0 || blockerAt > eventsAt {
		t.Fatalf("blocker at %d, events at %d:\n%s", blockerAt, eventsAt, decision.Reason)
	}
}

func TestBlockersDedupeOnIncidentAndSeqNotSeqAlone(t *testing.T) {
	// Two serve processes in one checkout can be in two DIFFERENT incidents and
	// both answer one peek. Sequence numbers are small per-incident counters, so
	// a collision is ordinary — and a bare-seq dedupe silently dropped the losing
	// session's real blocker on first-answer-wins.
	decision := BuildStopDecision([]SocketAnswer{
		quarantinePeek(t, "/a", "inc-a", 3),
		quarantinePeek(t, "/b", "inc-b", 3),
	}, 0)
	if got := strings.Count(decision.Reason, "seq 3 — the room QUARANTINED"); got != 2 {
		t.Fatalf("two incidents' blockers collapsed to %d line(s):\n%s", got, decision.Reason)
	}

	// The same incident answering twice DOES collapse.
	same := BuildStopDecision([]SocketAnswer{
		quarantinePeek(t, "/a", "inc-a", 3),
		quarantinePeek(t, "/b", "inc-a", 3),
	}, 0)
	if got := strings.Count(same.Reason, "seq 3 — the room QUARANTINED"); got != 1 {
		t.Fatalf("one incident's blocker appeared %d times:\n%s", got, same.Reason)
	}
}

func TestAnAnswerWithNoIncidentIDFallsBackToItsOwnSocketPath(t *testing.T) {
	// An older serve process, before `incidentId` was added to peek. It never
	// shares a scope with anything but itself — not with another old process, and
	// not with a new one. The duplicate is accepted; a dropped blocker is not.
	decision := BuildStopDecision([]SocketAnswer{
		quarantinePeek(t, "/old-a", "", 3),
		quarantinePeek(t, "/old-b", "", 3),
		quarantinePeek(t, "/new", "inc-a", 3),
	}, 0)
	if got := strings.Count(decision.Reason, "seq 3 — the room QUARANTINED"); got != 3 {
		t.Fatalf("got %d line(s), want 3 (one per scope):\n%s", got, decision.Reason)
	}
}

func TestAMerelyFlaggedItemNeverBlocks(t *testing.T) {
	// A flag is an open question; refusing every conclusion while one is open
	// would let any participant freeze an investigation.
	four := int64(4)
	answer := peekAnswer(t, "/s", PeekResponse{
		OK: true, V: SocketProtocolVersion, IncidentID: "inc-1", Cursor: 4, MaxSeq: 4, Digest: []string{},
		Attention: &client.Attention{FlaggedOwnContext: []client.FlaggedContext{{
			TargetSeq: &four, State: "flagged",
		}}},
	})
	if BuildStopDecision([]SocketAnswer{answer}, 0).Block {
		t.Fatal("a flagged (not quarantined) item must not block")
	}
}

// --- 3. the loop guard and the interrupted turn ------------------------------

func TestTheLoopGuardReadsStopHookActiveInEitherCasingAndDefaultsToFirstAttempt(t *testing.T) {
	if !IsStopHookActive(`{"stop_hook_active":true}`) {
		t.Fatal("snake_case guard")
	}
	if !IsStopHookActive(`{"stopHookActive":true}`) {
		t.Fatal("camelCase guard")
	}
	for _, input := range []string{`{"stop_hook_active":false}`, "", "not json at all", "{}"} {
		if IsStopHookActive(input) {
			t.Fatalf("%q must read as a first attempt", input)
		}
	}
}

func TestLoopCountIsTheGuardCursorHasSinceItSendsNoStopHookActive(t *testing.T) {
	for _, input := range []string{`{"loop_count":1}`, `{"loop_count":3}`} {
		if !IsStopHookActive(input) {
			t.Fatalf("%s must be guarded", input)
		}
	}
	if IsStopHookActive(`{"loop_count":0}`) {
		t.Fatal("the first Stop of a turn is not guarded")
	}
	// A Cursor version that sends no count is not thereby "already guarded" — it
	// falls back to consume-after-block plus Cursor's own followup cap.
	if IsStopHookActive(`{"hook_event_name":"stop","status":"completed"}`) {
		t.Fatal("no count means no guard")
	}
	if !IsStopHookActive(`{"stop_hook_active":true}`) {
		t.Fatal("the exit2 guard is untouched")
	}
}

func TestAnAbortedOrErroredTurnIsNotAConclusionWorthInterrupting(t *testing.T) {
	if !IsConcludedTurn(`{"status":"completed"}`) {
		t.Fatal("completed is a conclusion")
	}
	for _, input := range []string{`{"status":"aborted"}`, `{"status":"error"}`} {
		if IsConcludedTurn(input) {
			t.Fatalf("%s did not conclude", input)
		}
	}
	// Hosts that send no status at all are unaffected: absent means concluded.
	for _, input := range []string{"{}", "", "not json"} {
		if !IsConcludedTurn(input) {
			t.Fatalf("%q must read as concluded", input)
		}
	}
}

// --- 4. the handler ----------------------------------------------------------

// recorder captures the emits and consumes one run produces, in order.
type recorder struct {
	order   []string
	emitted []string
	sent    []SocketRequest
}

func (r *recorder) emit(text, channel string) {
	r.order = append(r.order, "emit")
	r.emitted = append(r.emitted, text)
	_ = channel
}

func (r *recorder) send(_ string, req SocketRequest) error {
	r.order = append(r.order, fmt.Sprintf("consume:%v", req.UpTo))
	r.sent = append(r.sent, req)
	return nil
}

func staticQuery(answers ...SocketAnswer) func(SocketRequest) ([]SocketAnswer, error) {
	return func(SocketRequest) ([]SocketAnswer, error) { return answers, nil }
}

func TestASessionCanAlwaysTerminateTheSecondStopAfterABlockIsAllowedThrough(t *testing.T) {
	answer := peekOfSession(t, "/s", sessionWith(t, 3, 1))
	r := &recorder{}
	first := RunStopHook(StopOptions{Input: "{}", Query: staticQuery(answer), Send: r.send, Emit: r.emit})
	if first.ExitCode != 2 {
		t.Fatalf("exit %d", first.ExitCode)
	}
	if len(r.sent) != 1 {
		t.Fatalf("sent %d consume(s)", len(r.sent))
	}

	second := RunStopHook(StopOptions{
		Input: `{"stop_hook_active":true}`,
		Query: func(SocketRequest) ([]SocketAnswer, error) {
			t.Fatal("the loop guard must short-circuit before any socket is touched")
			return nil, nil
		},
		Send: r.send,
		Emit: func(string, string) { t.Fatal("a guarded Stop must say nothing") },
	})
	if second.ExitCode != 0 {
		t.Fatalf("exit %d", second.ExitCode)
	}
}

func TestTheDigestIsEmittedBeforeAnyCursorMoves(t *testing.T) {
	r := &recorder{}
	RunStopHook(StopOptions{
		Query: staticQuery(peekOfSession(t, "/s", sessionWith(t, 1, 1))),
		Send:  r.send,
		Emit:  r.emit,
	})
	want := []string{"emit", "consume:1"}
	if strings.Join(r.order, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v — a crash mid-way must leave events queued, not consumed", r.order)
	}
}

func TestAFailedConsumeDoesNotSuppressABlockThatWasAlreadyEmitted(t *testing.T) {
	var emitted string
	res := RunStopHook(StopOptions{
		Query: staticQuery(peekOfSession(t, "/s", sessionWith(t, 1, 1))),
		Send:  func(string, SocketRequest) error { return errors.New("socket vanished") },
		Emit:  func(text, _ string) { emitted = text },
	})
	if res.ExitCode != 2 {
		t.Fatalf("exit %d", res.ExitCode)
	}
	if !strings.Contains(emitted, "Do not conclude yet") {
		t.Fatalf("got %q", emitted)
	}
}

func TestAHookThatCannotAskNeverBecomesAHookThatBlocks(t *testing.T) {
	res := RunStopHook(StopOptions{
		Query: func(SocketRequest) ([]SocketAnswer, error) { return nil, errors.New("permission denied") },
		Emit:  func(string, string) { t.Fatal("a failed query must stay silent") },
	})
	if res.ExitCode != 0 {
		t.Fatalf("exit %d", res.ExitCode)
	}
}

func TestNoServeSessionAnywhereCostsNothing(t *testing.T) {
	ws := tempWorkspace(t)
	if len(ListHookSockets(ws)) != 0 {
		t.Fatal("expected an empty workspace")
	}
	started := time.Now()
	res := RunStopHook(StopOptions{
		Query: workspaceQuery(ws),
		Emit:  func(string, string) { t.Fatal("nothing owed must say nothing") },
	})
	elapsed := time.Since(started)
	if res.ExitCode != 0 {
		t.Fatalf("exit %d", res.ExitCode)
	}
	if elapsed > 100*time.Millisecond {
		t.Fatalf("the empty path took %v — it must not wait on anything", elapsed)
	}
}

// --- 5. the handler under cursor-json ----------------------------------------

func TestUnderCursorJSONARefusalIsAFollowupMessageAndItStillConsumes(t *testing.T) {
	r := &recorder{}
	var channel string
	res := RunStopHook(StopOptions{
		Input:    `{"hook_event_name":"stop","status":"completed"}`,
		Protocol: CURSOR_JSON,
		Query:    staticQuery(peekOfSession(t, "/s", sessionWith(t, 2, 1))),
		Send:     r.send,
		Emit:     func(text, ch string) { channel = ch; r.emitted = append(r.emitted, text) },
	})

	if !res.Blocked || res.ExitCode != 0 {
		t.Fatalf("got %+v", res)
	}
	if channel != ChannelStdout {
		t.Fatalf("channel %q", channel)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(r.emitted[0]), &body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body["followup_message"], "Do not conclude yet — 2 update(s)") {
		t.Fatalf("got %q", body["followup_message"])
	}
	// consume-after-block is what bounds the followups.
	if len(r.sent) != 1 || r.sent[0].UpTo != float64(2) || r.sent[0].verb() != "consume" {
		t.Fatalf("got %+v", r.sent)
	}
}

func TestUnderCursorJSONAnAllowStillSpeaks(t *testing.T) {
	var emitted, channel string
	res := RunStopHook(StopOptions{
		Protocol: CURSOR_JSON,
		Query:    staticQuery(),
		Emit:     func(text, ch string) { emitted, channel = text, ch },
	})
	if res.ExitCode != 0 {
		t.Fatalf("exit %d", res.ExitCode)
	}
	if channel != ChannelStdout || strings.TrimSpace(emitted) != "{}" {
		t.Fatalf("silence would be a parse error: %q on %q", emitted, channel)
	}
}

func TestEveryEarlyExitUnderCursorJSONStillAnswers(t *testing.T) {
	cases := []struct {
		name  string
		input string
		query func(SocketRequest) ([]SocketAnswer, error)
	}{
		{"loop guard", `{"loop_count":1}`, staticQuery(peekOfSession(t, "/s", sessionWith(t, 2, 1)))},
		{"aborted turn", `{"status":"aborted"}`, staticQuery(peekOfSession(t, "/s", sessionWith(t, 2, 1)))},
		{"query failed", "", func(SocketRequest) ([]SocketAnswer, error) { return nil, errors.New("permission denied") }},
	}
	for _, c := range cases {
		var emitted string
		res := RunStopHook(StopOptions{
			Input:    c.input,
			Protocol: CURSOR_JSON,
			Query:    c.query,
			Send:     func(string, SocketRequest) error { t.Fatalf("%s must not consume", c.name); return nil },
			Emit:     func(text, _ string) { emitted = text },
		})
		if res.ExitCode != 0 {
			t.Fatalf("%s: exit %d", c.name, res.ExitCode)
		}
		if strings.TrimSpace(emitted) != "{}" {
			t.Fatalf("%s must still print one JSON object, got %q", c.name, emitted)
		}
	}
}

func TestAnAbortedTurnKeepsItsEventsQueuedForTheNextRealConclusion(t *testing.T) {
	s := sessionWith(t, 2, 1)
	RunStopHook(StopOptions{
		Input:    `{"status":"aborted"}`,
		Protocol: CURSOR_JSON,
		Query:    staticQuery(peekOfSession(t, "/s", s)),
		Send: func(string, SocketRequest) error {
			t.Fatal("an interrupted turn must not consume what it was never told")
			return nil
		},
		Emit: func(string, string) {},
	})
	if len(s.Pending()) != 2 {
		t.Fatalf("pending %d", len(s.Pending()))
	}
}

// --- 6. dispatch -------------------------------------------------------------

func TestRunHookEventRoutesStopToTheRegisteredHandler(t *testing.T) {
	// An empty workspace, so the handler takes the "nothing owed" path without a
	// serve process anywhere.
	ws := tempWorkspace(t)
	var emitted []string
	got := RunHookEvent(context.Background(), "stop", HookDeps{
		Workspace: ws,
		Host:      "claude-code",
		Emit:      func(text, _ string) { emitted = append(emitted, text) },
	})
	if got.ExitCode != 0 || got.Result != "allowed" {
		t.Fatalf("got %+v", got)
	}
	if len(emitted) != 0 {
		t.Fatalf("silence is the exit2 allow path, got %v", emitted)
	}
	// Under cursor-json the SAME allow speaks.
	emitted = nil
	got = RunHookEvent(context.Background(), "stop", HookDeps{
		Workspace: ws,
		Host:      "cursor",
		Emit:      func(text, _ string) { emitted = append(emitted, text) },
	})
	if got.ExitCode != 0 || len(emitted) != 1 || strings.TrimSpace(emitted[0]) != "{}" {
		t.Fatalf("got %+v / %v", got, emitted)
	}
}
