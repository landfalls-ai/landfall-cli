package narrate

import (
	"strings"
	"testing"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

func boolPtr(b bool) *bool { return &b }

func TestRenderFrame_NilFrame(t *testing.T) {
	got := RenderFrame(nil)
	want := "No incident context available."
	if got != want {
		t.Errorf("RenderFrame(nil) = %q, want %q", got, want)
	}
}

func TestRenderFrame_ZeroValueFrameNeverPanics(t *testing.T) {
	got := RenderFrame(&client.ContextFrame{})
	if !strings.Contains(got, "(untitled incident)") {
		t.Errorf("zero-value frame render = %q, want untitled-incident fallback", got)
	}
	if !strings.Contains(got, "No findings or open items yet") {
		t.Errorf("zero-value frame render = %q, want fresh-incident fallback", got)
	}
	if !strings.Contains(got, "freshness unknown") {
		t.Errorf("zero-value frame render = %q, want freshness unknown", got)
	}
	if !strings.Contains(got, "As of seq ? (") {
		t.Errorf("zero-value frame render = %q, want '?' asOfSeq", got)
	}
}

func TestRenderFrame_FullFrame(t *testing.T) {
	frame := &client.ContextFrame{
		Incident: client.Incident{
			Title:       "CloudFront 5xx spike",
			Severity:    "SEV-2",
			Status:      "investigating",
			AlertSource: "CloudWatch",
		},
		Brief: client.Brief{
			Established:   []client.BriefItem{{Seq: 3, Statement: "origin pool unhealthy", By: "Dana"}},
			WorkingTheory: []client.BriefItem{{Seq: 5, Statement: "cache stampede suspected", By: "Alex"}},
			Open:          []client.BriefItem{{Seq: 8, Statement: "confirm rollback safe", By: "Dana"}},
		},
		Participants: []client.Participant{
			{DisplayName: "Dana", Active: boolPtr(true)},
			{DisplayName: "Alex", EdgeAgentLabel: "claude-code", Active: boolPtr(false)},
			{DisplayName: "Robo", Active: nil},
		},
		FreshnessMs: f64Ptr(4500),
		AsOfSeq:     seqPtr(42),
	}
	got := RenderFrame(frame)

	for _, want := range []string{
		"CloudFront 5xx spike · SEV-2 · investigating",
		"Alert source: CloudWatch",
		"Established:",
		"  #3 origin pool unhealthy — Dana",
		"Open:",
		"  #5 cache stampede suspected — Alex",
		"  #8 confirm rollback safe — Dana",
		"Participants: Dana (active), Alex · claude-code (away), Robo (unknown)",
		"As of seq 42 (5s stale).",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderFrame output missing %q; full output:\n%s", want, got)
		}
	}
}

func TestRenderFrame_NoTitleFallsBackToUntitled(t *testing.T) {
	got := RenderFrame(&client.ContextFrame{})
	if !strings.HasPrefix(got, "(untitled incident)") {
		t.Errorf("got = %q, want to start with (untitled incident)", got)
	}
}

func TestFrameCursor(t *testing.T) {
	// "No cursor" is NoCursor (-1), not a pointer: it is the same value the
	// session's own cursor starts at, so handing it to AdvanceCursorTo is a
	// no-op rather than a rewind.
	if got := FrameCursor(nil); got != NoCursor {
		t.Errorf("FrameCursor(nil) = %v, want %v", got, NoCursor)
	}
	if got := FrameCursor(&client.ContextFrame{}); got != NoCursor {
		t.Errorf("FrameCursor(zero value) = %v, want %v", got, NoCursor)
	}
	f := &client.ContextFrame{AsOfSeq: seqPtr(99)}
	if got := FrameCursor(f); got != 99 {
		t.Errorf("FrameCursor = %v, want 99", got)
	}
	// Seq 0 is a real seq and must not be confused with "absent".
	zero := &client.ContextFrame{AsOfSeq: seqPtr(0)}
	if got := FrameCursor(zero); got != 0 {
		t.Errorf("FrameCursor(asOfSeq 0) = %v, want 0", got)
	}
}

func TestRenderDelta_EmptyReturnsEmptyString(t *testing.T) {
	if got := RenderDelta(nil); got != "" {
		t.Errorf("RenderDelta(nil) = %q, want empty", got)
	}
	if got := RenderDelta(&client.FrameDelta{}); got != "" {
		t.Errorf("RenderDelta(zero value) = %q, want empty", got)
	}
}

func TestRenderDelta_ItemsAndRoutineCount(t *testing.T) {
	delta := &client.FrameDelta{
		Items: []client.DeltaItem{
			{Seq: 10, Type: "claim.staged", Class: "addressed", By: "Dana", Summary: "origin is root cause"},
			{Seq: 11, Type: "edge.finding", Class: "substantive", By: "Alex"},
		},
		RoutineCount: 4,
	}
	got := RenderDelta(delta)
	if !strings.Contains(got, "⚠ 2 update(s) from other investigators since your last check:") {
		t.Errorf("missing header: %q", got)
	}
	if !strings.Contains(got, "➤ #10 claim.staged [Dana] — origin is root cause") {
		t.Errorf("missing addressed line: %q", got)
	}
	if !strings.Contains(got, "• #11 edge.finding [Alex]") {
		t.Errorf("missing routine-class line: %q", got)
	}
	if !strings.Contains(got, "(+4 routine update(s) — counted, not shown)") {
		t.Errorf("missing routine count line: %q", got)
	}
}

func TestRenderDelta_RoutineCountOnlyStillRenders(t *testing.T) {
	got := RenderDelta(&client.FrameDelta{RoutineCount: 3})
	if !strings.Contains(got, "⚠ 0 update(s)") || !strings.Contains(got, "(+3 routine update(s)") {
		t.Errorf("got = %q", got)
	}
}

func TestRenderSearchHits_NoHits(t *testing.T) {
	got := RenderSearchHits(nil, "5xx")
	want := `0 matching event(s) for "5xx".`
	if got != want {
		t.Errorf("RenderSearchHits(nil) = %q, want %q", got, want)
	}
	got2 := RenderSearchHits(&client.SearchResult{}, "5xx")
	if got2 != want {
		t.Errorf("RenderSearchHits(empty) = %q, want %q", got2, want)
	}
}

func TestRenderSearchHits_WithHits(t *testing.T) {
	result := &client.SearchResult{Hits: []client.SearchHit{
		{Seq: 4, Type: "edge.finding", By: "Dana", Snippet: "…origin pool 5xx…"},
	}}
	got := RenderSearchHits(result, "5xx")
	want := "1 matching event(s) for \"5xx\":\n#4 edge.finding [Dana] — …origin pool 5xx…"
	if got != want {
		t.Errorf("RenderSearchHits = %q, want %q", got, want)
	}
}

// --- the room's pinned scope (monorepo 20260921-101054) --------------------

func TestRenderFrame_OmitsScopeBlocksWhenServerSendsNone(t *testing.T) {
	// An older server sends no attachments/instructions/listening/focus at
	// all. The brief must look exactly as it did before, with no empty
	// headings and — the one that would actually mislead — no claim that
	// Beacon has stopped listening.
	got := RenderFrame(&client.ContextFrame{
		Incident: client.Incident{Title: "Checkout latency"},
		AsOfSeq:  seqPtr(7),
	})
	for _, absent := range []string{"Scope pinned", "Focus:", "not listening", "Organization instructions"} {
		if strings.Contains(got, absent) {
			t.Errorf("render of a frame without scope contains %q; full output:\n%s", absent, got)
		}
	}
}

func TestRenderFrame_RendersPinnedScope(t *testing.T) {
	frame := &client.ContextFrame{
		Incident: client.Incident{Title: "Checkout latency"},
		Attachments: []client.FrameAttachment{
			{Seq: 4, Kind: "repo", Label: "landfalls-ai/landfall", By: client.ActorRef{DisplayName: "Dana"}},
			{Seq: 6, Kind: "component", Ref: map[string]string{"elementId": "el-1"}, Label: "checkout-api", By: client.ActorRef{DisplayName: "Alex"}, Stale: true},
			{Seq: 9, Kind: "window", Ref: map[string]string{"until": "10:00", "since": "09:00"}, By: client.ActorRef{HumanActorID: "usr-3"}},
		},
		AsOfSeq: seqPtr(9),
	}
	got := RenderFrame(frame)
	for _, want := range []string{
		"Scope pinned to this room — read within it unless the person says otherwise:",
		"  repo: landfalls-ai/landfall (pinned by Dana)",
		"  component: checkout-api (pinned by Alex; it no longer resolves)",
		// No label from the server: the ref itself, keys sorted so two reads
		// of an unchanged room render identically.
		"  window: since=09:00 until=10:00 (pinned by usr-3)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderFrame output missing %q; full output:\n%s", want, got)
		}
	}
}

func TestRenderFrame_RendersFocusAndSilentBeacon(t *testing.T) {
	no := false
	got := RenderFrame(&client.ContextFrame{
		Incident:  client.Incident{Title: "Checkout latency"},
		Focus:     &client.FrameFocus{Focus: "  did the 10:02 rollback do it?  ", By: client.ActorRef{DisplayName: "Dana"}},
		Listening: &no,
		AsOfSeq:   seqPtr(9),
	})
	if !strings.Contains(got, "Focus: did the 10:02 rollback do it? (asked by Dana)") {
		t.Errorf("focus line missing or not trimmed; full output:\n%s", got)
	}
	if !strings.Contains(got, "Beacon is not listening to chat in this room") {
		t.Errorf("silent-Beacon line missing; full output:\n%s", got)
	}
}

func TestRenderFrame_ListeningTrueSaysNothing(t *testing.T) {
	yes := true
	got := RenderFrame(&client.ContextFrame{Incident: client.Incident{Title: "x"}, Listening: &yes})
	if strings.Contains(got, "listening") {
		t.Errorf("listening=true should render no line; full output:\n%s", got)
	}
}

func TestRenderFrame_RendersInstructionsWholeAndLabelsSections(t *testing.T) {
	frame := &client.ContextFrame{
		Incident: client.Incident{Title: "Checkout latency"},
		Attachments: []client.FrameAttachment{
			{Seq: 6, Kind: "component", Ref: map[string]string{"elementId": "el-1"}, Label: "checkout-api", By: client.ActorRef{DisplayName: "Alex"}},
		},
		Instructions: &client.FrameInstructions{
			Version: 4,
			Body:    "Never page the on-call before 07:00.\nQuote the runbook you used.",
			ComponentSections: map[string]string{
				"el-1": "Its cache is warmed by a cron at :05.",
				"el-9": "An unpinned component the server should not have sent.",
			},
		},
		AsOfSeq: seqPtr(9),
	}
	got := RenderFrame(frame)
	for _, want := range []string{
		"Organization instructions (version 4):",
		"  Never page the on-call before 07:00.",
		"  Quote the runbook you used.",
		// The element id is translated through the pinned attachment's label.
		"  For checkout-api:",
		"    Its cache is warmed by a cron at :05.",
		// The server filters componentSections to pinned components; this CLI
		// renders what it is given rather than re-deriving that rule.
		"  For el-9:",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderFrame output missing %q; full output:\n%s", want, got)
		}
	}
}

func TestRenderFrame_EmptyInstructionsDocumentRendersNothing(t *testing.T) {
	got := RenderFrame(&client.ContextFrame{
		Incident:     client.Incident{Title: "x"},
		Instructions: &client.FrameInstructions{Version: 2, Body: "   "},
	})
	if strings.Contains(got, "Organization instructions") {
		t.Errorf("an empty document should render no heading; full output:\n%s", got)
	}
}

func TestRenderFrame_ScopeStaysAboveTheFooter(t *testing.T) {
	// The "As of seq" line is the brief's terminator; anything appended after
	// it reads as a separate message rather than part of the brief.
	got := RenderFrame(&client.ContextFrame{
		Incident:    client.Incident{Title: "x"},
		Attachments: []client.FrameAttachment{{Seq: 1, Kind: "repo", Label: "a/b", By: client.ActorRef{DisplayName: "Dana"}}},
		AsOfSeq:     seqPtr(3),
	})
	if strings.Index(got, "Scope pinned") > strings.Index(got, "As of seq") {
		t.Errorf("scope rendered below the footer; full output:\n%s", got)
	}
}
