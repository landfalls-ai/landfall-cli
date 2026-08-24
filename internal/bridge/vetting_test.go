package bridge

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

// votingPub records any attempt to take a position, so the test can assert
// that none happens.
type votingPub struct {
	fakePub
	vmu       sync.Mutex
	positions int
	attention *client.Attention
}

func (v *votingPub) PositionClaim(context.Context, int64, string, string) error {
	v.vmu.Lock()
	v.positions++
	v.vmu.Unlock()
	return nil
}

func (v *votingPub) GetAttention(context.Context) (*client.Attention, error) {
	return v.attention, nil
}

func (v *votingPub) positionCount() int {
	v.vmu.Lock()
	defer v.vmu.Unlock()
	return v.positions
}

func seqp(n int64) *int64 { return &n }

// TestWorkerNeverCastsAVote is the point of vetting.go, and it is an assertion
// about what the code must NOT do.
//
// A vote is a position recorded under the responder's identity in a room where
// outcomes are computed from distinct participants. This worker has no model
// (D5), so any vote it cast would be based on no evidence-weighing at all —
// manufactured consensus, attributed to a human who never formed the view.
//
// Corroborating its own content would additionally be a no-op: admission
// already "never counts the claim author corroborating themselves".
func TestWorkerNeverCastsAVote(t *testing.T) {
	sp, mi, _ := rig(t)

	// A room actively asking for positions — the strongest pull toward voting.
	pub := &votingPub{
		fakePub: fakePub{instanceID: "agent-1"},
		attention: &client.Attention{
			VotesAwaited: []client.VoteAwaited{
				{ClaimSeq: seqp(4), Statement: "the rollback caused the 5xx wave", AuthoredBy: "Dana"},
				{ClaimSeq: seqp(7), Statement: "the cache stampede is downstream", AuthoredBy: "Sam"},
			},
		},
	}

	// And something of ours in flight, so it has its own content in play too.
	if _, err := sp.Accept("inc-1", "agent-1", "I claim the origin rollback at 14:20 caused it", nil); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	w := New(sp, mi, nil)
	w.Interval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx, pub, client.Config{IncidentID: "inc-1"})
	defer w.Stop()

	waitFor(t, func() bool { return pub.count() == 1 }, "the claim to publish")
	// Give several more sweeps a chance to misbehave.
	time.Sleep(100 * time.Millisecond)

	if n := pub.positionCount(); n != 0 {
		t.Fatalf("the worker cast %d vote(s): a model-free background process took an evidence-based "+
			"position under the responder's identity", n)
	}
}

// TestVoteRequestsAreSurfacedNotAnswered — the worker's job here is visibility,
// not decision. The request itself is `addressed` class, so D8 already delivers
// it to the main agent in band; this line is for an operator watching stderr.
func TestVoteRequestsAreSurfacedNotAnswered(t *testing.T) {
	sp, mi, _ := rig(t)

	var mu sync.Mutex
	var lines []string
	w := New(sp, mi, func(f string, a ...any) {
		mu.Lock()
		lines = append(lines, f)
		mu.Unlock()
	})
	w.Interval = 10 * time.Millisecond

	pub := &votingPub{
		fakePub: fakePub{instanceID: "agent-1"},
		attention: &client.Attention{
			VotesAwaited: []client.VoteAwaited{{ClaimSeq: seqp(4), Statement: "x", AuthoredBy: "Dana"}},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx, pub, client.Config{IncidentID: "inc-1"})

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, l := range lines {
			if strings.Contains(l, "awaiting your position") {
				return true
			}
		}
		return false
	}, "the pending vote to be surfaced")
	w.Stop()

	// And it must say the bridge is NOT handling it, so nobody assumes it was.
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "will not vote") {
		t.Errorf("the notice does not make clear the bridge is not answering: %q", joined)
	}
}

func TestFlaggedContentIsSurfaced(t *testing.T) {
	sp, mi, _ := rig(t)

	var mu sync.Mutex
	var lines []string
	w := New(sp, mi, func(f string, a ...any) { mu.Lock(); lines = append(lines, f); mu.Unlock() })
	w.Interval = 10 * time.Millisecond

	pub := &votingPub{
		fakePub:   fakePub{instanceID: "agent-1"},
		attention: &client.Attention{FlaggedOwnContext: []client.FlaggedContext{{}}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx, pub, client.Config{IncidentID: "inc-1"})

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, l := range lines {
			if strings.Contains(l, "flagged") {
				return true
			}
		}
		return false
	}, "the flag to be surfaced")
	w.Stop()
}

// TestExplicitClaimsAreStagedForVetting — staging is transcription of what the
// responder wrote, not a judgement about whether it is true.
func TestExplicitClaimsAreStagedForVetting(t *testing.T) {
	sp, mi, _ := rig(t)
	if _, err := sp.Accept("inc-1", "agent-1", "I claim the rollback at 14:20 caused the 5xx wave", nil); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	pub := &fakePub{instanceID: "agent-1"}
	w := New(sp, mi, nil)
	w.Interval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx, pub, client.Config{IncidentID: "inc-1"})
	defer w.Stop()

	waitFor(t, func() bool { return pub.count() == 1 }, "the claim to publish")

	pub.mu.Lock()
	defer pub.mu.Unlock()
	if pub.published[0]["stageAsClaim"] != true {
		t.Fatalf("an explicit claim was not staged for vetting: %v", pub.published[0])
	}
}

// TestOrdinaryFindingsAreNotStaged — staging costs the room attention. A plain
// observation must not drag other investigators into a vote.
func TestOrdinaryFindingsAreNotStaged(t *testing.T) {
	sp, mi, _ := rig(t)
	_, _ = sp.Accept("inc-1", "agent-1", "origin returned 502 and latency spiked hard", nil)

	pub := &fakePub{instanceID: "agent-1"}
	w := New(sp, mi, nil)
	w.Interval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx, pub, client.Config{IncidentID: "inc-1"})
	defer w.Stop()

	waitFor(t, func() bool { return pub.count() == 1 }, "the finding to publish")

	pub.mu.Lock()
	defer pub.mu.Unlock()
	if _, staged := pub.published[0]["stageAsClaim"]; staged {
		t.Fatal("an ordinary finding was staged for vetting; the room would be asked to vote on an observation")
	}
}

// TestAttentionFailureIsSilent — the worker's core job is queue in, publish
// out. A vetting read that fails must not produce noise or stop the drain.
func TestAttentionFailureIsSilent(t *testing.T) {
	sp, mi, _ := rig(t)

	var mu sync.Mutex
	var lines []string
	w := New(sp, mi, func(f string, a ...any) { mu.Lock(); lines = append(lines, f); mu.Unlock() })
	w.Interval = 10 * time.Millisecond

	// A publisher with no GetAttention at all — the interface is optional.
	pub := &fakePub{instanceID: "agent-1"}
	_, _ = sp.Accept("inc-1", "agent-1", "origin returned 502 and latency spiked", nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx, pub, client.Config{IncidentID: "inc-1"})

	waitFor(t, func() bool { return pub.count() == 1 }, "the publish to happen anyway")
	w.Stop()

	mu.Lock()
	defer mu.Unlock()
	if len(lines) != 0 {
		t.Errorf("a publisher without vetting support produced %d log line(s): %v", len(lines), lines)
	}
}
