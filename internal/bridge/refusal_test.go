package bridge

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/spool"
)

// runWorker starts a fast worker over pub for inc-1 and stops it with the test.
func runWorker(t *testing.T, sp *spool.Spool, pub Publisher) {
	t.Helper()
	_, mi, _ := rig(t)
	w := New(sp, mi, nil)
	w.Interval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { w.Stop(); cancel() })
	w.Start(ctx, pub, client.Config{IncidentID: "inc-1"})
}

// TestAClaimIsStagedWithTheStatementTheServerReads: the 2026-09-28 bug. The
// worker sent the contribution body ({text, publishedVia}) to POST /claims,
// the server answered 400 "statement required", and the entry was retried
// forever while the agent had been told "shared".
func TestAClaimIsStagedWithTheStatementTheServerReads(t *testing.T) {
	sp, _, _ := rig(t)
	const text = "the root cause is the origin rollback at 14:20"
	if _, err := sp.AcceptKind("inc-1", "agent-1", text, nil, nil, false, "claim"); err != nil {
		t.Fatalf("AcceptKind: %v", err)
	}
	pub := &fakePub{instanceID: "agent-1"}
	runWorker(t, sp, pub)

	waitFor(t, func() bool { return pub.stagedCount() == 1 }, "the claim to be staged")
	pub.mu.Lock()
	body := pub.staged[0]
	pub.mu.Unlock()
	if body["statement"] != text {
		t.Errorf("statement = %v, want the hand-off's text", body["statement"])
	}
	if body["reuseIfStaged"] != true {
		t.Errorf("reuseIfStaged = %v, want true so a republish after a crash cannot stage the claim twice", body["reuseIfStaged"])
	}
	if _, present := body["text"]; present {
		t.Errorf("the claim body still carries the contribution's `text`: %v", body)
	}
	if _, present := body["claimClass"]; present {
		t.Errorf("a hand-off nobody classed must leave the class to the server's conservative inference: %v", body)
	}
	if n := pub.count(); n != 0 {
		t.Errorf("the claim also went out as a contribution (%d)", n)
	}
}

// TestEveryClassifiedClaimMarkerStagesARealClaim: the text the classifier
// maps to a claim, with no explicit kind, takes the same fixed path.
func TestEveryClassifiedClaimMarkerStagesARealClaim(t *testing.T) {
	for _, text := range []string{
		"root cause is a config push to the edge fleet",
		"the root cause: connection pool exhaustion",
		"I claim the cache stampede started it",
		"claim: the deploy at 14:02 is responsible",
		"I'm confident the rollback caused the 5xx wave",
	} {
		t.Run(text, func(t *testing.T) {
			if Classify(text) != KindClaim {
				t.Fatalf("precondition: %q classifies as %q, not a claim", text, Classify(text))
			}
			sp, _, _ := rig(t)
			if _, err := sp.Accept("inc-1", "agent-1", text, nil); err != nil {
				t.Fatalf("Accept: %v", err)
			}
			pub := &fakePub{instanceID: "agent-1"}
			runWorker(t, sp, pub)
			waitFor(t, func() bool { return pub.stagedCount() == 1 }, "the claim to be staged")
			if refused, _ := sp.Refusals("inc-1"); len(refused) != 0 {
				t.Fatalf("the room refused the claim: %s", refused[0].Refusal)
			}
		})
	}
}

// TestAFailureDerivedClaimGoesInAsAFinding: POST /claims has no field for
// the agent's own "a tool call failed" self-report, so staging it there would
// drop the mark the admission gate screens on. It goes in as a finding, which
// carries the mark and is staged for vetting by the server all the same.
func TestAFailureDerivedClaimGoesInAsAFinding(t *testing.T) {
	e := &spool.Entry{Text: "the root cause is the rollback", Kind: "claim", SourceQueryFailed: true}
	if got := kindFor(e); got != KindFinding {
		t.Fatalf("kindFor = %q, want finding", got)
	}
	e.SourceQueryFailed = false
	if got := kindFor(e); got != KindClaim {
		t.Fatalf("an untainted claim must stay a claim, got %q", got)
	}

	sp, _, _ := rig(t)
	if _, err := sp.AcceptKind("inc-1", "agent-1", "the root cause is the rollback", nil, nil, true, "claim"); err != nil {
		t.Fatalf("AcceptKind: %v", err)
	}
	pub := &kindRecorder{fakePub: fakePub{instanceID: "agent-1"}}
	runWorker(t, sp, pub)
	waitFor(t, func() bool { return pub.count() == 1 }, "the finding to publish")
	if got := pub.kindAt(0); got != "finding" {
		t.Errorf("published as %q, want finding", got)
	}
	pub.mu.Lock()
	flag := pub.published[0]["sourceQueryFailed"]
	pub.mu.Unlock()
	if flag != true {
		t.Errorf("sourceQueryFailed = %v, want true on the wire", flag)
	}
	if pub.stagedCount() != 0 {
		t.Error("a failure-derived claim was staged without its tool-error mark")
	}
}

// statusPub answers every publish (contribution or claim) with one status,
// and the room's reason when one is set ("not now" otherwise).
type statusPub struct {
	fakePub
	status int
	reason string
	mu2    sync.Mutex
	tries  int
}

func (s *statusPub) answer(path string) error {
	s.mu2.Lock()
	s.tries++
	s.mu2.Unlock()
	reason := s.reason
	if reason == "" {
		reason = "not now"
	}
	return &client.HTTPError{Path: path, Status: s.status, Reason: reason}
}

func (s *statusPub) Contribute(context.Context, string, map[string]any) error {
	return s.answer("/edge/contributions")
}

func (s *statusPub) StageClaim(context.Context, map[string]any) error {
	return s.answer("/claims")
}

func (s *statusPub) attempts() int {
	s.mu2.Lock()
	defer s.mu2.Unlock()
	return s.tries
}

// TestNotNowIsRetriedNotRefused: a rate limit, a timeout, a server error or a
// write that lost a race with other writers in a busy room says nothing about
// the hand-off itself; the next sweep may land it. Only a refusal of the
// hand-off is final.
func TestNotNowIsRetriedNotRefused(t *testing.T) {
	for _, tc := range []struct {
		status int
		reason string
	}{
		{408, ""}, {429, ""}, {500, ""}, {503, ""},
		// edge.service.ts and admission.service.ts, verbatim.
		{409, "could not append edge event after retries"},
		{409, "could not append claim event after retries"},
	} {
		t.Run(fmt.Sprintf("HTTP_%d_%s", tc.status, tc.reason), func(t *testing.T) {
			sp, _, _ := rig(t)
			if _, err := sp.Accept("inc-1", "agent-1", "origin returned 502 and latency spiked", nil); err != nil {
				t.Fatalf("Accept: %v", err)
			}
			pub := &statusPub{fakePub: fakePub{instanceID: "agent-1"}, status: tc.status, reason: tc.reason}
			runWorker(t, sp, pub)
			waitFor(t, func() bool { return pub.attempts() >= 2 }, "a retry")
			if refused, _ := sp.Refusals("inc-1"); len(refused) != 0 {
				t.Fatalf("HTTP %d was treated as a refusal", tc.status)
			}
			pending, _ := sp.Pending("inc-1")
			if len(pending) != 1 {
				t.Fatalf("pending = %d, want the entry still queued", len(pending))
			}
		})
	}
}

// TestALostRaceOnAClaimIsStagedOnTheNextSweep: the claims endpoint answers 409
// when its append keeps losing to other writers. The claim is sent again, and
// reuseIfStaged is what makes that safe.
func TestALostRaceOnAClaimIsStagedOnTheNextSweep(t *testing.T) {
	sp, _, _ := rig(t)
	if _, err := sp.AcceptKind("inc-1", "agent-1", "the root cause is the origin rollback at 14:20", nil, nil, false, "claim"); err != nil {
		t.Fatalf("AcceptKind: %v", err)
	}
	pub := &statusPub{fakePub: fakePub{instanceID: "agent-1"}, status: 409, reason: "could not append claim event after retries"}
	runWorker(t, sp, pub)
	waitFor(t, func() bool { return pub.attempts() >= 2 }, "the claim to be sent again")
	if refused, _ := sp.Refusals("inc-1"); len(refused) != 0 {
		t.Fatalf("a lost race was recorded as a refusal: %q", refused[0].Refusal)
	}
}

// TestEveryOtherClientErrorIsARefusal: the same hand-off will get the same
// 4xx however often it is sent, so it is sent once. A 409 is one of these
// only when it names the room's state, as a closed engagement's does.
func TestEveryOtherClientErrorIsARefusal(t *testing.T) {
	for _, tc := range []struct {
		status int
		reason string
	}{
		{400, ""}, {403, ""}, {404, ""}, {413, ""}, {422, ""},
		{409, "engagement is closed; admission is frozen"},
	} {
		t.Run(fmt.Sprintf("HTTP_%d", tc.status), func(t *testing.T) {
			sp, _, _ := rig(t)
			if _, err := sp.Accept("inc-1", "agent-1", "origin returned 502 and latency spiked", nil); err != nil {
				t.Fatalf("Accept: %v", err)
			}
			pub := &statusPub{fakePub: fakePub{instanceID: "agent-1"}, status: tc.status, reason: tc.reason}
			runWorker(t, sp, pub)
			waitFor(t, func() bool {
				refused, _ := sp.Refusals("inc-1")
				return len(refused) == 1
			}, "the refusal to be recorded")
			time.Sleep(60 * time.Millisecond)
			if n := pub.attempts(); n != 1 {
				t.Errorf("sent %d times, want exactly once", n)
			}
			refused, _ := sp.Refusals("inc-1")
			if refused[0].RefusalStatus != tc.status {
				t.Errorf("recorded status %d, want %d", refused[0].RefusalStatus, tc.status)
			}
		})
	}
}

// TestAnExpiredSessionKeepsTheQueueForTheNextOne: the edge session token lives
// 8 hours and this CLI cannot renew it, so a long incident's shares draw 401.
// Nothing is wrong with them: they wait, marked, one request per sweep rather
// than one per entry, and land once the person rejoins with a new link (which
// starts the worker again over the same queue).
func TestAnExpiredSessionKeepsTheQueueForTheNextOne(t *testing.T) {
	sp, mi, _ := rig(t)
	for _, text := range []string{"origin returned 502 and latency spiked", "the retry storm began at 14:22Z"} {
		if _, err := sp.Accept("inc-1", "agent-1", text, nil); err != nil {
			t.Fatalf("Accept: %v", err)
		}
	}
	var mu sync.Mutex
	var lines []string
	w := New(sp, mi, func(format string, args ...any) {
		mu.Lock()
		lines = append(lines, fmt.Sprintf(format, args...))
		mu.Unlock()
	})
	w.Interval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { w.Stop(); cancel() })

	expired := &statusPub{fakePub: fakePub{instanceID: "agent-1"}, status: 401, reason: "Unauthorized"}
	w.Start(ctx, expired, client.Config{IncidentID: "inc-1"})
	waitFor(t, func() bool { return expired.attempts() >= 3 }, "several sweeps against the expired session")
	w.Stop()

	if refused, _ := sp.Refusals("inc-1"); len(refused) != 0 {
		t.Fatalf("an expired session was recorded as a refusal: %q", refused[0].Refusal)
	}
	gone, waiting, err := sp.AwaitingSession("inc-1")
	if err != nil || !gone || waiting != 2 {
		t.Fatalf("AwaitingSession = %v, %d, %v; want expired with both shares waiting", gone, waiting, err)
	}
	pending, _ := sp.Pending("inc-1")
	for _, e := range pending {
		if e.Text == "the retry storm began at 14:22Z" && e.Attempts != 0 {
			t.Errorf("the entry behind the expired one was sent %d times; the pass must end at the first 401", e.Attempts)
		}
	}
	mu.Lock()
	logged := 0
	for _, l := range lines {
		if strings.Contains(l, "session has expired") {
			logged++
		}
	}
	mu.Unlock()
	if logged != 1 {
		t.Errorf("the expiry was logged %d times, want once per run", logged)
	}

	// The person rejoins with a new link: a new session, the same queue.
	fresh := &fakePub{instanceID: "agent-1"}
	w.Start(ctx, fresh, client.Config{IncidentID: "inc-1"})
	waitFor(t, func() bool { return fresh.count() == 2 }, "both waiting shares to land on the new session")
	if gone, waiting, _ := sp.AwaitingSession("inc-1"); gone || waiting != 0 {
		t.Fatalf("after the rejoin AwaitingSession = %v, %d; want nothing waiting", gone, waiting)
	}
}

// TestARefusalWithNoReasonStillSaysWhatHappened: a bare 4xx carries no body;
// the agent is still told something true.
func TestARefusalWithNoReasonStillSaysWhatHappened(t *testing.T) {
	sp, _, _ := rig(t)
	if _, err := sp.Accept("inc-1", "agent-1", "origin returned 502 and latency spiked", nil); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	pub := &fakePub{instanceID: "agent-1", failWith: &client.HTTPError{Path: "/edge/contributions", Status: 403}}
	runWorker(t, sp, pub)
	waitFor(t, func() bool {
		refused, _ := sp.Refusals("inc-1")
		return len(refused) == 1
	}, "the refusal to be recorded")
	refused, _ := sp.Refusals("inc-1")
	if !strings.Contains(refused[0].Refusal, "403") {
		t.Errorf("refusal = %q, want the status named", refused[0].Refusal)
	}
}

// TestTakeRefusalsTellsEachRefusalOnce: the agent hears about a refusal on
// the next room tool result, and not again on every one after it.
func TestTakeRefusalsTellsEachRefusalOnce(t *testing.T) {
	sp, _, _ := rig(t)
	e, err := sp.Accept("inc-1", "agent-1", "chart data that did not fit", nil)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if err := sp.Refuse("inc-1", e.ID, 400, "widget data does not match the \"chart\" contract"); err != nil {
		t.Fatalf("Refuse: %v", err)
	}
	acc := NewAccepter(sp, nil)
	first := acc.TakeRefusals("inc-1")
	if len(first) != 1 || first[0].Text != "chart data that did not fit" || !strings.Contains(first[0].Reason, "chart") {
		t.Fatalf("first take = %+v", first)
	}
	if again := acc.TakeRefusals("inc-1"); len(again) != 0 {
		t.Fatalf("a refusal was told twice: %+v", again)
	}
	if other := acc.TakeRefusals("inc-2"); len(other) != 0 {
		t.Fatalf("another room's take returned %+v", other)
	}
}
