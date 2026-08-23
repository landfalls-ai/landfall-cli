package narrate

import (
	"strings"
	"testing"
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
	got := RenderFrame(&ContextFrame{})
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
	frame := &ContextFrame{
		Incident: Incident{
			Title:       "CloudFront 5xx spike",
			Severity:    "SEV-2",
			Status:      "investigating",
			AlertSource: "CloudWatch",
		},
		Brief: Brief{
			Established:   []BriefItem{{Seq: 3, Statement: "origin pool unhealthy", By: "Dana"}},
			WorkingTheory: []BriefItem{{Seq: 5, Statement: "cache stampede suspected", By: "Alex"}},
			Open:          []BriefItem{{Seq: 8, Statement: "confirm rollback safe", By: "Dana"}},
		},
		Participants: []Participant{
			{DisplayName: "Dana", Active: boolPtr(true)},
			{DisplayName: "Alex", EdgeAgentLabel: "claude-code", Active: boolPtr(false)},
			{DisplayName: "Robo", Active: nil},
		},
		FreshnessMs: f64Ptr(4500),
		AsOfSeq:     intPtr(42),
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
	got := RenderFrame(&ContextFrame{})
	if !strings.HasPrefix(got, "(untitled incident)") {
		t.Errorf("got = %q, want to start with (untitled incident)", got)
	}
}

func TestFrameCursor(t *testing.T) {
	if got := FrameCursor(nil); got != nil {
		t.Errorf("FrameCursor(nil) = %v, want nil", got)
	}
	if got := FrameCursor(&ContextFrame{}); got != nil {
		t.Errorf("FrameCursor(zero value) = %v, want nil", got)
	}
	f := &ContextFrame{AsOfSeq: intPtr(99)}
	if got := FrameCursor(f); got == nil || *got != 99 {
		t.Errorf("FrameCursor = %v, want 99", got)
	}
}

func TestRenderDelta_EmptyReturnsEmptyString(t *testing.T) {
	if got := RenderDelta(nil); got != "" {
		t.Errorf("RenderDelta(nil) = %q, want empty", got)
	}
	if got := RenderDelta(&FrameDelta{}); got != "" {
		t.Errorf("RenderDelta(zero value) = %q, want empty", got)
	}
}

func TestRenderDelta_ItemsAndRoutineCount(t *testing.T) {
	delta := &FrameDelta{
		Items: []DeltaItem{
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
	got := RenderDelta(&FrameDelta{RoutineCount: 3})
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
	got2 := RenderSearchHits(&SearchResult{}, "5xx")
	if got2 != want {
		t.Errorf("RenderSearchHits(empty) = %q, want %q", got2, want)
	}
}

func TestRenderSearchHits_WithHits(t *testing.T) {
	result := &SearchResult{Hits: []SearchHit{
		{Seq: 4, Type: "edge.finding", By: "Dana", Snippet: "…origin pool 5xx…"},
	}}
	got := RenderSearchHits(result, "5xx")
	want := "1 matching event(s) for \"5xx\":\n#4 edge.finding [Dana] — …origin pool 5xx…"
	if got != want {
		t.Errorf("RenderSearchHits = %q, want %q", got, want)
	}
}
