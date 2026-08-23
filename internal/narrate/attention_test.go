package narrate

import (
	"strings"
	"testing"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

func seqPtr(i int64) *int64     { return &i }
func f64Ptr(f float64) *float64 { return &f }

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		ms   float64
		want string
	}{
		{0, "0m"},
		{-1000, "0m"},
		{12 * 60_000, "12m"},
		{135 * 60_000, "2h 15m"},
		{59_999, "0m"}, // < 1 minute floors to 0m
		{60_000, "1m"},
	}
	for _, c := range cases {
		if got := FormatDuration(c.ms); got != c.want {
			t.Errorf("FormatDuration(%v) = %q, want %q", c.ms, got, c.want)
		}
	}
}

func TestFormatDuration_NonFinite(t *testing.T) {
	if got := FormatDuration(nan()); got != "0m" {
		t.Errorf("FormatDuration(NaN) = %q, want 0m", got)
	}
	if got := FormatDuration(posInf()); got != "0m" {
		t.Errorf("FormatDuration(+Inf) = %q, want 0m", got)
	}
}

func TestVoteKey(t *testing.T) {
	if got := VoteKey(client.VoteAwaited{ClaimSeq: seqPtr(5), Stale: false}); got != "5:fresh" {
		t.Errorf("VoteKey fresh = %q", got)
	}
	if got := VoteKey(client.VoteAwaited{ClaimSeq: seqPtr(5), Stale: true}); got != "5:stale" {
		t.Errorf("VoteKey stale = %q", got)
	}
	if got := VoteKey(client.VoteAwaited{}); got != "undefined:fresh" {
		t.Errorf("VoteKey missing claimSeq = %q", got)
	}
}

func TestVoteRequestBlock_Empty(t *testing.T) {
	b := VoteRequestBlock(&client.Attention{}, nil, 0)
	if b.Text != "" || len(b.Keys) != 0 {
		t.Errorf("VoteRequestBlock({}) = %#v, want zero value", b)
	}
	// A nil projection is "nothing known to be waiting" — an unreachable
	// server must leave the tool result untouched, never panic.
	if b := VoteRequestBlock(nil, nil, 0); b.Text != "" || len(b.Keys) != 0 {
		t.Errorf("VoteRequestBlock(nil) = %#v, want zero value", b)
	}
	if blockers := StopBlockers(nil); HasStopBlockers(blockers) {
		t.Errorf("StopBlockers(nil) = %#v, want nothing blocking", blockers)
	}
}

func TestVoteRequestBlock_FiltersInvalidAndNotified(t *testing.T) {
	att := &client.Attention{VotesAwaited: []client.VoteAwaited{
		{ClaimSeq: nil, Statement: "no seq, filtered"},
		{ClaimSeq: seqPtr(1), Statement: "already notified"},
		{ClaimSeq: seqPtr(2), Statement: "fresh one"},
	}}
	notified := map[string]bool{"1:fresh": true}
	b := VoteRequestBlock(att, notified, 0)
	if !strings.Contains(b.Text, "fresh one") {
		t.Errorf("expected fresh claim in text, got %q", b.Text)
	}
	if strings.Contains(b.Text, "already notified") || strings.Contains(b.Text, "no seq") {
		t.Errorf("filtered entries leaked into text: %q", b.Text)
	}
	if len(b.Keys) != 1 || b.Keys[0] != "2:fresh" {
		t.Errorf("Keys = %v, want [2:fresh]", b.Keys)
	}
}

func TestVoteRequestBlock_CapsAtMaxAndReportsOmitted(t *testing.T) {
	att := &client.Attention{VotesAwaited: []client.VoteAwaited{
		{ClaimSeq: seqPtr(1), Statement: "one"},
		{ClaimSeq: seqPtr(2), Statement: "two"},
		{ClaimSeq: seqPtr(3), Statement: "three"},
		{ClaimSeq: seqPtr(4), Statement: "four"},
	}}
	b := VoteRequestBlock(att, nil, VoteLinesMax)
	if len(b.Keys) != 3 {
		t.Errorf("Keys len = %d, want 3", len(b.Keys))
	}
	if !strings.Contains(b.Text, "+1 more claim(s) awaiting your position") {
		t.Errorf("text missing omitted-count line: %q", b.Text)
	}
	if strings.Contains(b.Text, "four") {
		t.Errorf("4th claim should have been capped out: %q", b.Text)
	}
}

func TestVoteRequestBlock_RendersStaleAndExpiry(t *testing.T) {
	att := &client.Attention{VotesAwaited: []client.VoteAwaited{
		{ClaimSeq: seqPtr(1), Statement: "stale one", Stale: true},
		{ClaimSeq: seqPtr(2), Statement: "expiring one", ExpiresInMs: f64Ptr(135 * 60_000)},
	}}
	b := VoteRequestBlock(att, nil, 0)
	if !strings.Contains(b.Text, "PAST its freshness window") {
		t.Errorf("missing stale marker: %q", b.Text)
	}
	if !strings.Contains(b.Text, "2h 15m left") {
		t.Errorf("missing expiry duration: %q", b.Text)
	}
}

func TestVoteRequestBlock_IncludesAuthorAndShortfall(t *testing.T) {
	att := &client.Attention{VotesAwaited: []client.VoteAwaited{
		{
			ClaimSeq:      seqPtr(9),
			Statement:     "origin is root cause",
			AuthoredBy:    "Dana",
			AuthorIsAgent: true,
			Shortfall:     &client.Shortfall{Text: "needs a second corroboration"},
		},
	}}
	b := VoteRequestBlock(att, nil, 0)
	if !strings.Contains(b.Text, "from Dana (agent)") {
		t.Errorf("missing author attribution: %q", b.Text)
	}
	if !strings.Contains(b.Text, "needs a second corroboration") {
		t.Errorf("missing shortfall text: %q", b.Text)
	}
}

func TestDivergenceKey(t *testing.T) {
	got := DivergenceKey(client.Divergence{EstablishedSubject: "origin pool", ObservedSubject: "cache layer"})
	want := "origin pool::cache layer"
	if got != want {
		t.Errorf("DivergenceKey = %q, want %q", got, want)
	}
}

func TestDivergenceBlock_SilentWhenNotDiverging(t *testing.T) {
	if b := DivergenceBlock(nil, nil); b.Text != "" {
		t.Errorf("nil divergence should be silent, got %q", b.Text)
	}
	if b := DivergenceBlock(&client.Divergence{Diverging: false}, nil); b.Text != "" {
		t.Errorf("diverging:false should be silent, got %q", b.Text)
	}
}

func TestDivergenceBlock_SilentWhenAlreadyNotified(t *testing.T) {
	d := &client.Divergence{Diverging: true, EstablishedSubject: "origin pool", ObservedSubject: "cache layer"}
	notified := map[string]bool{DivergenceKey(*d): true}
	if b := DivergenceBlock(d, notified); b.Text != "" {
		t.Errorf("already-notified pair should be silent, got %q", b.Text)
	}
}

func TestDivergenceBlock_RendersInvitation(t *testing.T) {
	d := &client.Divergence{Diverging: true, EstablishedSubject: "origin pool", ObservedSubject: "cache layer"}
	b := DivergenceBlock(d, nil)
	if !strings.Contains(b.Text, `"origin pool"`) || !strings.Contains(b.Text, `"cache layer"`) {
		t.Errorf("text missing subjects: %q", b.Text)
	}
	if !strings.Contains(b.Text, "invitation, not a block") {
		t.Errorf("text missing invitation framing: %q", b.Text)
	}
	if len(b.Keys) != 1 || b.Keys[0] != "origin pool::cache layer" {
		t.Errorf("Keys = %v", b.Keys)
	}
}

func TestDivergenceBlock_DefaultsSubjectsWhenAbsent(t *testing.T) {
	d := &client.Divergence{Diverging: true}
	b := DivergenceBlock(d, nil)
	if !strings.Contains(b.Text, "a different subject") || !strings.Contains(b.Text, "something else") {
		t.Errorf("missing default subject wording: %q", b.Text)
	}
}

func TestStopBlockers_QuarantinedButNotMerelyFlagged(t *testing.T) {
	att := &client.Attention{FlaggedOwnContext: []client.FlaggedContext{
		{TargetSeq: seqPtr(1), State: "flagged"},
		{TargetSeq: seqPtr(2), State: "quarantined"},
	}}
	blockers := StopBlockers(att)
	if len(blockers.Quarantined) != 1 || blockers.Quarantined[0].TargetSeq == nil || *blockers.Quarantined[0].TargetSeq != 2 {
		t.Errorf("Quarantined = %#v, want only seq 2", blockers.Quarantined)
	}
	if !HasStopBlockers(blockers) {
		t.Error("HasStopBlockers = false, want true")
	}
}

func TestStopBlockers_MerelyFlaggedNeverBlocksAlone(t *testing.T) {
	att := &client.Attention{FlaggedOwnContext: []client.FlaggedContext{{TargetSeq: seqPtr(1), State: "flagged"}}}
	blockers := StopBlockers(att)
	if HasStopBlockers(blockers) {
		t.Error("a merely-flagged item must never block")
	}
}

func TestStopBlockers_ContradictionBlocksOrdinaryVoteDoesNot(t *testing.T) {
	att := &client.Attention{VotesAwaited: []client.VoteAwaited{
		{ClaimSeq: seqPtr(1), Statement: "ordinary vote, no contradiction"},
		{
			ClaimSeq:  seqPtr(2),
			Statement: "contradicts the record",
			Shortfall: &client.Shortfall{Missing: &client.Missing{Contradiction: []int64{7, 8}}},
		},
	}}
	blockers := StopBlockers(att)
	if len(blockers.Contradictions) != 1 || blockers.Contradictions[0].ClaimSeq == nil || *blockers.Contradictions[0].ClaimSeq != 2 {
		t.Errorf("Contradictions = %#v, want only claim 2", blockers.Contradictions)
	}
	if !HasStopBlockers(blockers) {
		t.Error("HasStopBlockers = false, want true")
	}
}

func TestHasStopBlockers_FalseWhenEmpty(t *testing.T) {
	if HasStopBlockers(Blockers{}) {
		t.Error("HasStopBlockers(empty) = true, want false")
	}
}

func TestDescribeStopBlockers_RendersBothClasses(t *testing.T) {
	blockers := Blockers{
		Quarantined: []client.FlaggedContext{
			{TargetSeq: seqPtr(12), TargetKind: "finding", Relation: "cited", CitedByClaimSeqs: []int64{3, 4}, Reason: "debunked"},
		},
		Contradictions: []client.VoteAwaited{
			{
				ClaimSeq:  seqPtr(9),
				Statement: "origin is root cause",
				Shortfall: &client.Shortfall{Missing: &client.Missing{Contradiction: []int64{2}}},
			},
		},
	}
	lines := DescribeStopBlockers(blockers, 0)
	if len(lines) != 2 {
		t.Fatalf("lines = %v, want 2", lines)
	}
	if !strings.Contains(lines[0], "seq 12") || !strings.Contains(lines[0], "QUARANTINED") ||
		!strings.Contains(lines[0], "cited by your claims #3, #4") || !strings.Contains(lines[0], "debunked") {
		t.Errorf("quarantined line = %q", lines[0])
	}
	if !strings.Contains(lines[1], "claim #9") || !strings.Contains(lines[1], "contradicts admitted claim(s) #2") {
		t.Errorf("contradiction line = %q", lines[1])
	}
}

func TestDescribeStopBlockers_DefaultsMissingKindAndRelation(t *testing.T) {
	blockers := Blockers{Quarantined: []client.FlaggedContext{{TargetSeq: seqPtr(1)}}}
	lines := DescribeStopBlockers(blockers, 0)
	if len(lines) != 1 || !strings.Contains(lines[0], "this item you used") {
		t.Errorf("lines = %v, want default kind/relation wording", lines)
	}
}

func TestDescribeStopBlockers_CapsAtMax(t *testing.T) {
	blockers := Blockers{Quarantined: []client.FlaggedContext{
		{TargetSeq: seqPtr(1)}, {TargetSeq: seqPtr(2)}, {TargetSeq: seqPtr(3)},
	}}
	lines := DescribeStopBlockers(blockers, 2)
	if len(lines) != 3 {
		t.Fatalf("lines = %v, want 3 (2 shown + 1 omitted-count)", lines)
	}
	if !strings.Contains(lines[2], "+1 more — call get_updates for the rest.") {
		t.Errorf("last line = %q", lines[2])
	}
}

func TestTouchesAttention(t *testing.T) {
	if !TouchesAttention("claim.staged") {
		t.Error("claim.staged should touch attention")
	}
	if !TouchesAttention("context.flagged") {
		t.Error("context.flagged should touch attention")
	}
	if TouchesAttention("edge.finding") {
		t.Error("edge.finding should not touch attention")
	}
	if TouchesAttention("") {
		t.Error("empty type should not touch attention")
	}
}

func nan() float64    { var z float64; return z / z }
func posInf() float64 { var z float64; return 1 / z }
