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

// statusPub answers every publish with one status.
type statusPub struct {
	fakePub
	status int
	mu2    sync.Mutex
	tries  int
}

func (s *statusPub) Contribute(context.Context, string, map[string]any) error {
	s.mu2.Lock()
	s.tries++
	s.mu2.Unlock()
	return &client.HTTPError{Path: "/edge/contributions", Status: s.status, Reason: "not now"}
}

func (s *statusPub) attempts() int {
	s.mu2.Lock()
	defer s.mu2.Unlock()
	return s.tries
}

// TestNotNowIsRetriedNotRefused: a rate limit, a timeout or a server error
// says nothing about the hand-off itself; the next sweep may land it. Only a
// refusal of the hand-off is final.
func TestNotNowIsRetriedNotRefused(t *testing.T) {
	for _, status := range []int{408, 429, 500, 503} {
		t.Run(fmt.Sprintf("HTTP_%d", status), func(t *testing.T) {
			sp, _, _ := rig(t)
			if _, err := sp.Accept("inc-1", "agent-1", "origin returned 502 and latency spiked", nil); err != nil {
				t.Fatalf("Accept: %v", err)
			}
			pub := &statusPub{fakePub: fakePub{instanceID: "agent-1"}, status: status}
			runWorker(t, sp, pub)
			waitFor(t, func() bool { return pub.attempts() >= 2 }, "a retry")
			if refused, _ := sp.Refusals("inc-1"); len(refused) != 0 {
				t.Fatalf("HTTP %d was treated as a refusal", status)
			}
			pending, _ := sp.Pending("inc-1")
			if len(pending) != 1 {
				t.Fatalf("pending = %d, want the entry still queued", len(pending))
			}
		})
	}
}

// TestEveryOtherClientErrorIsARefusal: the same hand-off will get the same
// 4xx however often it is sent, so it is sent once.
func TestEveryOtherClientErrorIsARefusal(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 409, 413, 422} {
		t.Run(fmt.Sprintf("HTTP_%d", status), func(t *testing.T) {
			sp, _, _ := rig(t)
			if _, err := sp.Accept("inc-1", "agent-1", "origin returned 502 and latency spiked", nil); err != nil {
				t.Fatalf("Accept: %v", err)
			}
			pub := &statusPub{fakePub: fakePub{instanceID: "agent-1"}, status: status}
			runWorker(t, sp, pub)
			waitFor(t, func() bool {
				refused, _ := sp.Refusals("inc-1")
				return len(refused) == 1
			}, "the refusal to be recorded")
			time.Sleep(60 * time.Millisecond)
			if n := pub.attempts(); n != 1 {
				t.Errorf("sent %d times, want exactly once", n)
			}
		})
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
	if err := sp.Refuse("inc-1", e.ID, "widget data does not match the \"chart\" contract"); err != nil {
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
