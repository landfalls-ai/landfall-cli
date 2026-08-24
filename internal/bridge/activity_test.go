package bridge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

// TestWorkerNarratesWhatItPublishes — T030a. Without this the room gets a
// participant that acts but never speaks: serve's 15s heartbeat keeps the tile
// ALIVE with a generic "investigating", but the descriptive per-action line
// disappears when record_activity moves off the main agent.
func TestWorkerNarratesWhatItPublishes(t *testing.T) {
	sp, mi, _ := rig(t)
	if _, err := sp.Accept("inc-1", "agent-1", "origin returned 502 and latency spiked hard", nil); err != nil {
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

	said := pub.beatsSaid()
	if len(said) == 0 {
		t.Fatal("the worker published without narrating: the room sees activity from a silent participant")
	}
	if !strings.Contains(said[0], "finding") {
		t.Errorf("activity line %q does not say what is being shared", said[0])
	}
}

// TestNarrationFailureDoesNotStopThePublish — the ordering that matters. The
// responder's finding reaching the room is the point; the line announcing it is
// a courtesy. A courtesy must never block the point.
func TestNarrationFailureDoesNotStopThePublish(t *testing.T) {
	sp, mi, _ := rig(t)
	_, _ = sp.Accept("inc-1", "agent-1", "the connection pool is exhausted on replica 3", nil)

	pub := &fakePub{instanceID: "agent-1", beatErr: errors.New("presence unreachable")}
	w := New(sp, mi, nil)
	w.Interval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx, pub, client.Config{IncidentID: "inc-1"})
	defer w.Stop()

	waitFor(t, func() bool { return pub.count() == 1 },
		"the publish to happen despite narration failing")
}

func TestActivityLineMatchesTheKind(t *testing.T) {
	cases := map[Kind]string{
		KindClaim:  "claim",
		KindWidget: "widget",
		KindNote:   "note",
	}
	for kind, want := range cases {
		got := narrateHandOff(kind, "some text")
		if !strings.Contains(got, want) {
			t.Errorf("narrateHandOff(%q) = %q, want it to mention %q", kind, got, want)
		}
	}
}

// TestActivityLineIsBounded — this lands in a participant strip other people
// read under pressure. The full text arrives moments later as the published
// item, so a long preview costs everyone attention and buys nothing.
func TestActivityLineIsBounded(t *testing.T) {
	long := strings.Repeat("the origin service returned an error and then ", 20)
	got := narrateHandOff(KindFinding, long)

	if len([]rune(got)) > 100 {
		t.Errorf("activity line is %d runes: %q", len([]rune(got)), got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("a truncated line should say it was truncated: %q", got)
	}
}

// TestExcerptDoesNotSplitCodepoints — truncating mid-rune renders as a
// replacement character in every viewer.
func TestExcerptDoesNotSplitCodepoints(t *testing.T) {
	// Multi-byte throughout, longer than the 60-rune bound.
	text := strings.Repeat("日", 100)
	got := firstClause(text)

	if strings.ContainsRune(got, '�') {
		t.Fatalf("excerpt contains a replacement character: %q", got)
	}
	if len([]rune(strings.TrimSuffix(got, "…"))) > 60 {
		t.Errorf("excerpt is %d runes, want <= 60", len([]rune(got)))
	}
}

func TestExcerptPrefersASentenceBoundary(t *testing.T) {
	got := firstClause("origin returned 502. then the pool filled up and everything queued behind it.")
	if got != "origin returned 502" {
		t.Errorf("firstClause = %q, want it to stop at the sentence boundary", got)
	}
}

func TestExcerptHandlesEmptyAndWhitespace(t *testing.T) {
	for _, input := range []string{"", "   ", "\n\t "} {
		if got := firstClause(input); got != "" {
			t.Errorf("firstClause(%q) = %q, want empty", input, got)
		}
	}
	// And the caller must still produce a usable line.
	if got := narrateHandOff(KindFinding, "  "); got != "sharing a finding" {
		t.Errorf("narrateHandOff with blank text = %q", got)
	}
}
