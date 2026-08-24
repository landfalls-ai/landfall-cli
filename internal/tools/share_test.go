package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/landfalls-ai/landfall-cli/internal/mcp"
	"github.com/landfalls-ai/landfall-cli/internal/session"
)

type fakeAccepter struct {
	incidentID string
	agentID    string
	text       string
	refs       []string
	err        error
	redacted   bool
	calls      int
}

func (f *fakeAccepter) Accept(incidentID, agentInstanceID, text string, refs []string) (string, bool, error) {
	f.calls++
	f.incidentID, f.agentID, f.text, f.refs = incidentID, agentInstanceID, text, refs
	if f.err != nil {
		return "", false, f.err
	}
	return "entry-1", f.redacted, nil
}

// TestShareTellsTheResponderWhenTextWasRedacted — FR-010's human half. Removing
// the secret is necessary; the responder not knowing their words changed is a
// separate failure, and they would find out from the timeline.
func TestShareTellsTheResponderWhenTextWasRedacted(t *testing.T) {
	acc := &fakeAccepter{redacted: true}
	tool := shareTool(t, newSession(&fakeClient{}), acc)

	out, err := tool.Handler(context.Background(), map[string]any{"text": "password=hunter2correct"})
	if err != nil {
		t.Fatalf("share: %v", err)
	}
	if !strings.Contains(out, "redacted") {
		t.Fatalf("redaction was silent: %q", out)
	}

	// And the ordinary path must NOT cry wolf — a false alarm every time trains
	// people to ignore the real one.
	acc2 := &fakeAccepter{}
	tool2 := shareTool(t, newSession(&fakeClient{}), acc2)
	out2, err := tool2.Handler(context.Background(), map[string]any{"text": "origin returned 502"})
	if err != nil {
		t.Fatalf("share: %v", err)
	}
	if strings.Contains(out2, "redacted") {
		t.Errorf("clean text reported as redacted: %q", out2)
	}
}

func shareTool(t *testing.T, sess *session.Session, acc Accepter) mcp.Tool {
	t.Helper()
	for _, tl := range BuildWithAccepter(sess, acc) {
		if tl.Name == "share_with_room" {
			return tl
		}
	}
	t.Fatal("share_with_room not registered")
	return mcp.Tool{}
}

// TestShareIsRegisteredUnwrapped is the load-bearing structural assertion.
//
// Every other publishing tool goes through b.narrated(), which heartbeats and
// may post a durable contribution BEFORE the handler runs (wrapper.go:53-71) —
// network work on the calling path. FR-001 requires share_with_room to return
// without any of that. The un-wrapped pattern already exists (join_war_room,
// tools.go:112-116), so this is a registration choice, not wrapper surgery —
// and this test is what stops someone "tidying" it into consistency later.
func TestShareIsRegisteredUnwrapped(t *testing.T) {
	sess := session.New(session.Options{})
	acc := &fakeAccepter{}

	tool := shareTool(t, sess, acc)

	_, err := tool.Handler(context.Background(), map[string]any{"text": "x"})
	if err == nil {
		t.Fatal("expected an error when not joined")
	}
	if acc.calls != 0 {
		t.Error("accepted a hand-off with no joined room: it would strand the finding")
	}

	// The real assertion is on BEHAVIOUR, not on error text.
	//
	// An earlier version of this test checked that the error mentioned
	// "join_war_room" — useless, because errNotConnected (wrapper.go:21) says
	// exactly the same thing, so wrapping the tool in narrated() still passed.
	// Mutation testing caught it. FR-001's actual property is "no network I/O
	// before returning", so assert that: a wrapped tool heartbeats before its
	// handler runs (wrapper.go:53-55); an un-wrapped one cannot.
	joined := &fakeClient{}
	tool = shareTool(t, newSession(joined), acc)

	if _, err := tool.Handler(context.Background(), map[string]any{"text": "a real finding"}); err != nil {
		t.Fatalf("share: %v", err)
	}
	if beats := joined.recorded("heartbeat"); len(beats) != 0 {
		t.Fatalf("share_with_room made %d network call(s) before returning — it is going through narrated(), which breaks FR-001", len(beats))
	}
	if posts := joined.recorded("contribute"); len(posts) != 0 {
		t.Fatalf("share_with_room posted %d contribution(s) on the calling path", len(posts))
	}
}

func TestShareRequiresText(t *testing.T) {
	acc := &fakeAccepter{}
	tool := shareTool(t, session.New(session.Options{}), acc)

	_, err := tool.Handler(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected an error for a missing text argument")
	}
	if acc.calls != 0 {
		t.Error("queued an empty hand-off")
	}
}

// TestQueueFullSaysItWasNotRecorded — FR-012. The responder must never be told
// something reached the room when it did not.
func TestQueueFullSaysItWasNotRecorded(t *testing.T) {
	acc := &fakeAccepter{err: ErrQueueFull}
	sess := session.New(session.Options{Client: &fakeClient{}})
	tool := shareTool(t, sess, acc)

	_, err := tool.Handler(context.Background(), map[string]any{"text": "important"})
	if err == nil {
		t.Fatal("a full queue reported success")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "not") {
		t.Fatalf("error %q does not make clear the finding was NOT recorded", err)
	}
}

func TestShareQueuesAndReturnsWithoutRoomContent(t *testing.T) {
	acc := &fakeAccepter{}
	sess := session.New(session.Options{Client: &fakeClient{}})
	tool := shareTool(t, sess, acc)

	out, err := tool.Handler(context.Background(), map[string]any{
		"text": "origin returned 502",
		"refs": []any{"src/gateway.go:42", "abc123"},
	})
	if err != nil {
		t.Fatalf("share: %v", err)
	}
	if acc.calls != 1 || acc.text != "origin returned 502" {
		t.Fatalf("hand-off not queued: calls=%d text=%q", acc.calls, acc.text)
	}
	if len(acc.refs) != 2 || acc.refs[0] != "src/gateway.go:42" {
		t.Fatalf("refs not passed through: %v", acc.refs)
	}

	// FR-002b: a hand-off is not a delivery vehicle. One short line, no digest,
	// no vote request, no divergence nudge.
	if strings.Count(out, "\n") > 0 {
		t.Errorf("multi-line result suggests room content rode back: %q", out)
	}
	if len(out) > 120 {
		t.Errorf("result is %d chars; it should be one short ack", len(out))
	}
}

func TestRefsToleratesBothSliceShapes(t *testing.T) {
	for name, refs := range map[string]any{
		"json []any":  []any{"a", "b"},
		"go []string": []string{"a", "b"},
	} {
		got := refsOf(map[string]any{"refs": refs})
		if len(got) != 2 || got[0] != "a" {
			t.Errorf("%s: refsOf = %v", name, got)
		}
	}
	if got := refsOf(map[string]any{}); got != nil {
		t.Errorf("absent refs = %v, want nil", got)
	}
}

// TestNilAccepterOmitsTheTool — registering a tool that accepts a hand-off and
// drops it would be worse than not having the verb at all.
func TestNilAccepterOmitsTheTool(t *testing.T) {
	for _, tl := range Build(session.New(session.Options{})) {
		if tl.Name == "share_with_room" {
			t.Fatal("share_with_room registered with no queue behind it")
		}
	}
}

// TestBuildSurfaceIsUnchangedWithoutAccepter pins that this feature adds
// nothing to the shipped tool surface until it is wired on.
func TestBuildSurfaceIsUnchangedWithoutAccepter(t *testing.T) {
	sess := session.New(session.Options{})
	plain := Build(sess)
	withNil := BuildWithAccepter(sess, nil)

	if len(plain) != len(withNil) {
		t.Fatalf("Build=%d BuildWithAccepter(nil)=%d", len(plain), len(withNil))
	}
	for i := range plain {
		if plain[i].Name != withNil[i].Name {
			t.Fatalf("tool %d differs: %q vs %q", i, plain[i].Name, withNil[i].Name)
		}
	}
}
