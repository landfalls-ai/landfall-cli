package narrate

import (
	"regexp"
	"strings"
	"testing"
)

// Ported 1:1 from test/narrate.test.mjs (feature 20260812-010632, US5/T050a,
// FR-029): the no-evidence marker on templated content, in the one renderer
// both the in-band tool-result block and the idle-path digest share.

func TestIsTemplated_ReadsExplicitMarkerNotSynthesizedAlone(t *testing.T) {
	if got := IsTemplated(Args{"synthesized": false, "disclosedAsTemplated": true}); got != true {
		t.Errorf("IsTemplated(synthesized:false, disclosedAsTemplated:true) = %v, want true", got)
	}
	// synthesized:false ALONE (no explicit marker) must not be treated as
	// templated — the server sets both together deliberately; reading
	// synthesized alone would be a second, drifting interpretation of a
	// field this package does not own.
	if got := IsTemplated(Args{"synthesized": false}); got != false {
		t.Errorf("IsTemplated(synthesized:false) = %v, want false", got)
	}
	if got := IsTemplated(Args{}); got != false {
		t.Errorf("IsTemplated({}) = %v, want false", got)
	}
	if got := IsTemplated(nil); got != false {
		t.Errorf("IsTemplated(nil) = %v, want false", got)
	}
}

func TestFormatEventLine_AppendsNoEvidenceMarkerForTemplatedContent(t *testing.T) {
	line := FormatEventLine(Event{
		Seq:  25,
		Type: "agent.message",
		Payload: Args{
			"text":                 "current hypothesis (50%): …",
			"synthesized":          false,
			"disclosedAsTemplated": true,
		},
	})
	if !regexp.MustCompile(`#25 agent\.message`).MatchString(line) {
		t.Errorf("line = %q, want to match #25 agent\\.message", line)
	}
	if !strings.HasSuffix(line, "(no evidence — templated, not analysis)") {
		t.Errorf("line = %q, want suffix '(no evidence — templated, not analysis)'", line)
	}
}

func TestFormatEventLine_NoMarkerForOrdinaryContent(t *testing.T) {
	line := FormatEventLine(Event{
		Seq:  26,
		Type: "edge.finding",
		Payload: Args{
			"text":        "origin pool unhealthy",
			"displayName": "Dana",
		},
	})
	if strings.Contains(line, "no evidence") {
		t.Errorf("line = %q, want no 'no evidence' marker", line)
	}
}

func TestFormatEventLine_NoPayloadAtAll(t *testing.T) {
	got := FormatEventLine(Event{Seq: 1, Type: "incident.opened"})
	want := "#1 incident.opened"
	if got != want {
		t.Errorf("FormatEventLine = %q, want %q", got, want)
	}
}

func TestEventText_UnaffectedByTemplatedMarker(t *testing.T) {
	got := EventText(Args{
		"text":                 `Re: "what are you doing?" — …`,
		"synthesized":          false,
		"disclosedAsTemplated": true,
	})
	want := `Re: "what are you doing?" — …`
	if got != want {
		t.Errorf("EventText = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------- extra coverage

func TestFormatEventLine_AttributesToActor(t *testing.T) {
	got := FormatEventLine(Event{
		Seq:  10,
		Type: "edge.finding",
		Payload: Args{
			"text":           "origin pool unhealthy",
			"displayName":    "Dana",
			"edgeAgentLabel": "claude-code",
		},
	})
	want := "#10 edge.finding [Dana · claude-code] — origin pool unhealthy"
	if got != want {
		t.Errorf("FormatEventLine = %q, want %q", got, want)
	}
}

func TestEventActor_DropsFalsyParts(t *testing.T) {
	if got := EventActor(Args{"edgeAgentLabel": "claude-code"}); got != "claude-code" {
		t.Errorf("EventActor = %q, want %q", got, "claude-code")
	}
	if got := EventActor(Args{}); got != "" {
		t.Errorf("EventActor = %q, want empty", got)
	}
}

func TestEventText_FallsThroughFieldChain(t *testing.T) {
	cases := []struct {
		name    string
		payload Args
		want    string
	}{
		{"text wins", Args{"text": "a", "description": "b"}, "a"},
		{"description fallback", Args{"description": "b"}, "b"},
		{"doing fallback", Args{"doing": "c"}, "c"},
		{"summary fallback", Args{"summary": "d"}, "d"},
		{"statement fallback", Args{"statement": "e"}, "e"},
		{"reason fallback", Args{"reason": "f"}, "f"},
		{"stance fallback", Args{"stance": "g"}, "g"},
		{"nothing present", Args{}, ""},
		{"first non-null field non-string yields empty", Args{"text": 5}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := EventText(c.payload); got != c.want {
				t.Errorf("EventText(%v) = %q, want %q", c.payload, got, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------- NarrateDoing

func TestNarrateDoing(t *testing.T) {
	cases := []struct {
		name string
		tool string
		args Args
		want string
	}{
		{"get_brief", "get_brief", nil, "reviewing the incident brief"},
		{"get_updates", "get_updates", nil, "checking for new shared context"},
		{"read_timeline", "read_timeline", nil, "reading the incident timeline"},
		{"search_context with query", "search_context", Args{"query": "5xx"}, `searching context for "5xx"`},
		{"search_context without query", "search_context", nil, "searching context"},
		{"post_finding with text", "post_finding", Args{"text": "origin unhealthy"}, "posting a finding: origin unhealthy"},
		{"post_finding without text", "post_finding", nil, "posting a finding"},
		{"note always shows text (possibly empty)", "note", nil, "noting: "},
		{"note with text", "note", Args{"text": "checking logs"}, "noting: checking logs"},
		{"propose_action with description", "propose_action", Args{"description": "roll back"}, "proposing a remediation: roll back"},
		{"propose_action without description", "propose_action", nil, "proposing a remediation"},
		{"post_widget with title", "post_widget", Args{"title": "Latency"}, "building a dashboard widget: Latency"},
		{"post_widget without title", "post_widget", nil, "building a dashboard widget"},
		{"upload_artifact with filename", "upload_artifact", Args{"filename": "report.csv"}, `sharing an artifact: "report.csv"`},
		{"upload_artifact without filename", "upload_artifact", nil, "sharing an artifact"},
		{"flag_context with reason", "flag_context", Args{"targetSeq": 7, "reason": "stale data"}, "flagging #7 as wrong: stale data"},
		{"flag_context without reason", "flag_context", Args{"targetSeq": 7}, "flagging #7 as wrong"},
		{"flag_context without targetSeq", "flag_context", nil, "flagging #undefined as wrong"},
		{"corroborate_claim", "corroborate_claim", Args{"claimSeq": 3, "reason": "matches logs"}, "corroborating claim #3: matches logs"},
		{"corroborate_claim no reason", "corroborate_claim", Args{"claimSeq": 3}, "corroborating claim #3"},
		{"contest_claim", "contest_claim", Args{"claimSeq": 4, "reason": "contradicts metric"}, "contesting claim #4: contradicts metric"},
		{"contest_claim no reason", "contest_claim", Args{"claimSeq": 4}, "contesting claim #4"},
		{"stage_claim with statement", "stage_claim", Args{"statement": "origin is root cause"}, "staging a claim: origin is root cause"},
		{"stage_claim without statement", "stage_claim", nil, "staging a claim"},
		{"record_activity with doing", "record_activity", Args{"doing": "grepping logs"}, "grepping logs"},
		{"record_activity without doing", "record_activity", nil, "investigating"},
		{"unknown tool", "some_future_tool", nil, "using some_future_tool"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NarrateDoing(c.tool, c.args); got != c.want {
				t.Errorf("NarrateDoing(%q, %v) = %q, want %q", c.tool, c.args, got, c.want)
			}
		})
	}
}

func TestNarrateDoing_TruncatesLongText(t *testing.T) {
	long := strings.Repeat("a", 200)
	got := NarrateDoing("post_finding", Args{"text": long})
	wantSuffix := strings.Repeat("a", 79) + "…"
	if !strings.HasSuffix(got, wantSuffix) {
		t.Errorf("NarrateDoing did not truncate: got %q", got)
	}
	if len([]rune(got)) != len("posting a finding: ")+80 {
		t.Errorf("truncated length wrong: got %d runes", len([]rune(got)))
	}
}

// ---------------------------------------------------------------- ContributionFor

func TestContributionFor(t *testing.T) {
	if c := ContributionFor("post_finding", Args{"text": "x", "resource": "s3://bucket"}); c == nil {
		t.Fatal("post_finding: want non-nil contribution")
	} else if c.Kind != "finding" {
		t.Errorf("post_finding kind = %q, want finding", c.Kind)
	} else if body, ok := c.Body.(FindingBody); !ok || body.Text != "x" || body.Resource != "s3://bucket" {
		t.Errorf("post_finding body = %#v", c.Body)
	}

	if c := ContributionFor("note", Args{"text": "y"}); c == nil || c.Kind != "finding" {
		t.Errorf("note: want finding contribution, got %#v", c)
	}

	if c := ContributionFor("search_context", Args{"query": "5xx"}); c == nil {
		t.Fatal("search_context: want non-nil contribution")
	} else if body, ok := c.Body.(QueryBody); !ok || body.Source != "edge" || body.Operation != "search" || body.Resource != "5xx" {
		t.Errorf("search_context body = %#v", c.Body)
	}

	if c := ContributionFor("propose_action", Args{"description": "d", "dryRunPreview": "p"}); c == nil {
		t.Fatal("propose_action: want non-nil contribution")
	} else if body, ok := c.Body.(ActionBody); !ok || body.Description != "d" || body.DryRunPreview != "p" {
		t.Errorf("propose_action body = %#v", c.Body)
	}

	if c := ContributionFor("post_widget", Args{"widgetType": "stat", "title": "t", "data": map[string]any{"a": 1}}); c == nil {
		t.Fatal("post_widget: want non-nil contribution")
	} else if body, ok := c.Body.(WidgetBody); !ok || body.WidgetType != "stat" || body.Title != "t" {
		t.Errorf("post_widget body = %#v", c.Body)
	}

	// upload_artifact and all four vetting tools must return nil: their
	// endpoints append their own durable events server-side.
	for _, tool := range []string{"upload_artifact", "flag_context", "corroborate_claim", "contest_claim", "stage_claim"} {
		if c := ContributionFor(tool, Args{"reason": "x", "statement": "y"}); c != nil {
			t.Errorf("%s: want nil contribution (server appends its own event), got %#v", tool, c)
		}
	}

	// Read-only tools: presence only.
	for _, tool := range []string{"get_brief", "read_timeline", "get_updates", "record_activity"} {
		if c := ContributionFor(tool, nil); c != nil {
			t.Errorf("%s: want nil contribution, got %#v", tool, c)
		}
	}
}

func TestContributionFor_NoteAndFinding_DefaultEmptyTextOnMissing(t *testing.T) {
	c := ContributionFor("post_finding", nil)
	if c == nil {
		t.Fatal("want non-nil contribution")
	}
	body, ok := c.Body.(FindingBody)
	if !ok || body.Text != "" {
		t.Errorf("body = %#v, want empty text", c.Body)
	}
}
