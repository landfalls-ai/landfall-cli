package bridge

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

// kindRecorder captures the kind each publish went out as, which the plain
// fakePub does not.
type kindRecorder struct {
	fakePub
	mu    sync.Mutex
	kinds []string
}

func (k *kindRecorder) Contribute(ctx context.Context, kind string, body map[string]any) error {
	k.mu.Lock()
	k.kinds = append(k.kinds, kind)
	k.mu.Unlock()
	return k.fakePub.Contribute(ctx, kind, body)
}

func (k *kindRecorder) kindAt(i int) string {
	k.mu.Lock()
	defer k.mu.Unlock()
	if i >= len(k.kinds) {
		return ""
	}
	return k.kinds[i]
}

// TestPublishedItemsCarryBridgeProvenance — FR-004 / SC-003. Without this the
// room cannot tell a responder's direct action from their bridge's publish.
func TestPublishedItemsCarryBridgeProvenance(t *testing.T) {
	sp, mi, _ := rig(t)
	if _, err := sp.Accept("inc-1", "agent-1", "origin returned 502 and latency spiked", nil); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	pub := &fakePub{instanceID: "agent-1"}
	w := New(sp, mi, nil)
	w.Interval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx, pub, client.Config{IncidentID: "inc-1"})
	defer w.Stop()

	waitFor(t, func() bool { return pub.count() == 1 }, "the hand-off to publish")

	pub.mu.Lock()
	defer pub.mu.Unlock()
	got, ok := pub.published[0]["publishedVia"]
	if !ok {
		t.Fatal("published item carries no publishedVia: the room cannot distinguish bridge from direct")
	}
	if got != "bridge" {
		t.Fatalf("publishedVia = %v, want \"bridge\"", got)
	}
}

// TestProvenanceIsAFieldNotASecondIdentity — spec D1, and the reason SC-005
// holds. A bridge publish must go out under the responder's SAME
// agentInstanceId. A second identity would let one human corroborate their own
// claim, inflating vetting quorum silently.
func TestProvenanceIsAFieldNotASecondIdentity(t *testing.T) {
	sp, mi, _ := rig(t)
	_, _ = sp.Accept("inc-1", "agent-1", "the connection pool is exhausted on replica 3", nil)

	pub := &fakePub{instanceID: "agent-1"}
	w := New(sp, mi, nil)
	w.Interval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx, pub, client.Config{IncidentID: "inc-1"})
	defer w.Stop()

	waitFor(t, func() bool { return pub.count() == 1 }, "the hand-off to publish")

	pub.mu.Lock()
	defer pub.mu.Unlock()
	body := pub.published[0]
	for _, forbidden := range []string{"agentInstanceId", "actor", "identity", "as"} {
		if v, present := body[forbidden]; present {
			t.Fatalf("publish body sets %q=%v — the bridge must not present a second room identity (D1); "+
				"two identities for one human would inflate vetting quorum", forbidden, v)
		}
	}
}

// TestPublishUsesTheClassifiedKind — the worker must not file everything as a
// finding. A passing remark filed as a finding clutters the timeline other
// investigators read under pressure.
func TestPublishUsesTheClassifiedKind(t *testing.T) {
	sp, mi, _ := rig(t)
	if _, err := sp.Accept("inc-1", "agent-1", "checking the dashboard", nil); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	pub := &kindRecorder{fakePub: fakePub{instanceID: "agent-1"}}
	w := New(sp, mi, nil)
	w.Interval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx, pub, client.Config{IncidentID: "inc-1"})
	defer w.Stop()

	waitFor(t, func() bool { return pub.count() == 1 }, "the aside to publish")

	if got := pub.kindAt(0); got != string(KindNote) {
		t.Fatalf("an aside was published as %q, want note — everything-is-a-finding buries the real ones", got)
	}
}

// TestWorkerNeverPublishesAnAction — the end-to-end half of D2. classify_test
// proves Classify cannot return one; this proves the worker cannot send one
// either, whatever a responder types.
func TestWorkerNeverPublishesAnAction(t *testing.T) {
	sp, mi, _ := rig(t)
	for _, text := range []string{
		"please run kubectl rollout undo deployment/checkout",
		"remediation: drain node-7 and cordon it",
		"execute: terraform apply -auto-approve",
	} {
		if _, err := sp.Accept("inc-1", "agent-1", text, nil); err != nil {
			t.Fatalf("Accept: %v", err)
		}
	}

	pub := &kindRecorder{fakePub: fakePub{instanceID: "agent-1"}}
	w := New(sp, mi, nil)
	w.Interval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx, pub, client.Config{IncidentID: "inc-1"})
	defer w.Stop()

	waitFor(t, func() bool { return pub.count() == 3 }, "all three to publish")

	for i := 0; i < 3; i++ {
		switch pub.kindAt(i) {
		case "action", "remediation", "propose_action":
			t.Fatalf("publish %d went out as %q: a background worker initiated an approval-gated act", i, pub.kindAt(i))
		}
	}
}
