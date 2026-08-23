package narrate

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Ported from src/attention.mjs — what the room is waiting on from THIS
// agent, and how that reaches it (landfalls-ai/landfall#252, story #203).
//
// The projection itself lives on the server (GET …/vetting/attention), which
// is the whole point: quorum, admission and decay are the platform's rules,
// and a re-derivation here would be a second implementation of them.
// Everything in this file is presentation and a decision about when to
// interrupt.
//
// TWO CHANNELS, ONE SOURCE.
//
//   tier 0 (piggyback) — a "vote requested" line prepended to a tool result.
//                        Cheap, unmissable, costs the agent nothing.
//   tier 1 (Stop hook) — a refusal to conclude while something the agent
//                        RELIED ON has been quarantined, or a claim that
//                        contradicts the admitted record is still unanswered.
//
// EVERY FUNCTION HERE IS PURE. No clock, no I/O, no module state.

// VoteLinesMax is the cap on vote-request lines spelled out in full on one
// tool result (VOTE_LINES_MAX in attention.mjs).
const VoteLinesMax = 3

// AttentionPrefixes are the event type prefixes whose arrival can change
// what awaits this agent (ATTENTION_PREFIXES in attention.mjs).
var AttentionPrefixes = []string{"claim.", "context."}

// TouchesAttention reports whether a room event could change the attention
// projection — used to refresh on arrival rather than on a timer.
func TouchesAttention(eventType string) bool {
	for _, p := range AttentionPrefixes {
		if strings.HasPrefix(eventType, p) {
			return true
		}
	}
	return false
}

// Missing names what a staged claim's shortfall is missing before it can be
// admitted; Contradiction, when non-empty, lists the admitted claim seqs it
// contradicts.
type Missing struct {
	Contradiction []int
}

// Shortfall is why a staged claim is not yet admitted.
type Shortfall struct {
	Text    string
	Missing *Missing
}

// VoteRequest is one entry in Attention.VotesAwaited: a staged claim awaiting
// this agent's position. ClaimSeq is a pointer because the source filters
// out any entry whose claimSeq is not a number — a nil ClaimSeq here plays
// that same "invalid/missing" role.
type VoteRequest struct {
	ClaimSeq      *int
	Stale         bool
	AuthoredBy    string
	AuthorIsAgent bool
	Statement     string
	Shortfall     *Shortfall
	// ExpiresInMs is a float64 (not int) so a genuinely non-finite or absent
	// value can be represented and rejected exactly like Number.isFinite
	// does in formatDuration/voteRequestBlock; nil means absent.
	ExpiresInMs *float64
}

// FlaggedContext is one entry in Attention.FlaggedOwnContext: content this
// agent authored or cited that has been flagged (and possibly quarantined).
type FlaggedContext struct {
	TargetSeq        int
	State            string // e.g. "quarantined"
	TargetKind       string
	Relation         string
	CitedByClaimSeqs []int
	Reason           string
}

// Attention is the server's vetting/attention projection for this agent.
type Attention struct {
	VotesAwaited      []VoteRequest
	FlaggedOwnContext []FlaggedContext
}

// Block is a piggyback text block plus the dedupe keys it announced, so a
// caller can mark them notified only once the block is actually delivered.
type Block struct {
	Text string
	Keys []string
}

// VoteKey is the key a vote request is remembered by, so the same claim is
// not re-announced on every tool call. Staleness is part of the key on
// purpose: a claim about to lapse is genuinely new information, and it is
// the last moment a position can still count.
func VoteKey(v VoteRequest) string {
	seq := "undefined"
	if v.ClaimSeq != nil {
		seq = strconv.Itoa(*v.ClaimSeq)
	}
	fresh := "fresh"
	if v.Stale {
		fresh = "stale"
	}
	return seq + ":" + fresh
}

// VoteRequestBlock builds the piggyback block for a tool result.
//
// notified is the set of keys already announced to this session; it is read
// here, never mutated — the caller decides when to record the returned Keys
// as notified, so a caller that fails to deliver the block does not lose the
// announcement. max <= 0 defaults to VoteLinesMax.
//
// Returns a zero-value Block (Text == "") when there is nothing new to say.
func VoteRequestBlock(attention Attention, notified map[string]bool, max int) Block {
	if max <= 0 {
		max = VoteLinesMax
	}
	var fresh []VoteRequest
	for _, v := range attention.VotesAwaited {
		if v.ClaimSeq == nil {
			continue
		}
		if notified[VoteKey(v)] {
			continue
		}
		fresh = append(fresh, v)
	}
	if len(fresh) == 0 {
		return Block{}
	}

	shown := fresh
	if len(fresh) > max {
		shown = fresh[:max]
	}
	omitted := len(fresh) - len(shown)

	lines := make([]string, 0, len(shown)+2)
	for _, v := range shown {
		who := ""
		if v.AuthoredBy != "" {
			who = " from " + v.AuthoredBy
			if v.AuthorIsAgent {
				who += " (agent)"
			}
		}
		need := ""
		if v.Shortfall != nil && v.Shortfall.Text != "" {
			need = " — " + oneLine(v.Shortfall.Text, 100)
		}
		when := ""
		switch {
		case v.Stale:
			when = " — PAST its freshness window"
		case v.ExpiresInMs != nil && *v.ExpiresInMs > 0:
			when = fmt.Sprintf(" — %s left", FormatDuration(*v.ExpiresInMs))
		}
		lines = append(lines, fmt.Sprintf(`⚠ vote requested: claim #%d "%s"%s%s%s`,
			*v.ClaimSeq, oneLine(v.Statement, 120), who, need, when))
	}
	if omitted > 0 {
		lines = append(lines, fmt.Sprintf("+%d more claim(s) awaiting your position — see the war room.", omitted))
	}
	lines = append(lines,
		"Take a position with corroborate_claim or contest_claim when you have evidence either way; "+
			"your vote is a position, never a decision, so vote and carry on.")

	keys := make([]string, len(shown))
	for i, v := range shown {
		keys[i] = VoteKey(v)
	}
	return Block{Text: strings.Join(lines, "\n"), Keys: keys}
}

// Divergence is the server's vetting/divergence read for this agent's recent
// activity: whether it is drifting from the room's admitted causal claim.
type Divergence struct {
	Diverging          bool
	EstablishedSubject string
	ObservedSubject    string
}

// DivergenceKey is the key a divergence nudge is remembered by, so the same
// established/observed pair is not re-announced on every tool call.
func DivergenceKey(d Divergence) string {
	return d.EstablishedSubject + "::" + d.ObservedSubject
}

// DivergenceBlock builds the piggyback block for a diverging:true read, or a
// zero-value Block when there is nothing to say — silent by construction
// when divergence is nil, Diverging is false, or the pair was already
// announced this session.
//
// This is a THIRD tier-0 piggyback, alongside vote requests: an INVITATION,
// exactly like a vote request — never a tier-1 Stop-hook block (StopBlockers
// below is untouched by this). De-duplication is this session's own
// responsibility (via notified), not the server's — a stateless server has
// no notion of "already told this terminal".
func DivergenceBlock(divergence *Divergence, notified map[string]bool) Block {
	if divergence == nil || !divergence.Diverging {
		return Block{}
	}
	key := DivergenceKey(*divergence)
	if notified[key] {
		return Block{}
	}
	established := "a different subject"
	if divergence.EstablishedSubject != "" {
		established = `"` + oneLine(divergence.EstablishedSubject, 100) + `"`
	}
	observed := "something else"
	if divergence.ObservedSubject != "" {
		observed = `"` + oneLine(divergence.ObservedSubject, 100) + `"`
	}
	text := fmt.Sprintf(
		"↷ the room's established root cause is %s; your recent reads have been about %s — "+
			"worth a look before you go further. (This is an invitation, not a block — carry on if your work genuinely does not depend on it.)",
		established, observed)
	return Block{Text: text, Keys: []string{key}}
}

// FormatDuration renders "2h 15m", "12m" — locale-free, no dependency on
// system locale settings. ms is a float64 so a non-finite or non-positive
// input (matching Number.isFinite(ms) || ms <= 0 in the source) safely
// yields "0m" rather than a garbage string.
func FormatDuration(ms float64) string {
	if math.IsNaN(ms) || math.IsInf(ms, 0) || ms <= 0 {
		return "0m"
	}
	minutes := math.Floor(ms / 60000)
	hours := math.Floor(minutes / 60)
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", int64(hours), int64(minutes)%60)
	}
	return fmt.Sprintf("%dm", int64(minutes))
}

// Blockers is the two blocking classes StopBlockers returns.
type Blockers struct {
	Quarantined    []FlaggedContext
	Contradictions []VoteRequest
}

// StopBlockers returns the two things that must stop a conclusion:
//
// (a) QUARANTINED CONTEXT THIS AGENT IS ATTACHED TO — flagged-own-context
//
//	entries whose state is "quarantined". This also covers an item the
//	agent AUTHORED and had quarantined (the ticket's "cited a now-
//	quarantined seq" plus this case, since the failure is identical). A
//	merely "flagged" item never blocks: a flag is an open question, and
//	refusing every conclusion while one is open would let any participant
//	freeze an investigation.
//
// (b) AN UNANSWERED CONTRADICTION — a staged claim awaiting this agent's
//
//	position whose shortfall names a contradiction against the admitted
//	record. Ordinary vote requests do NOT block; most claims do not need
//	this participant.
func StopBlockers(attention Attention) Blockers {
	var quarantined []FlaggedContext
	for _, f := range attention.FlaggedOwnContext {
		if f.State == "quarantined" {
			quarantined = append(quarantined, f)
		}
	}
	var contradictions []VoteRequest
	for _, v := range attention.VotesAwaited {
		if v.Shortfall != nil && v.Shortfall.Missing != nil && len(v.Shortfall.Missing.Contradiction) > 0 {
			contradictions = append(contradictions, v)
		}
	}
	return Blockers{Quarantined: quarantined, Contradictions: contradictions}
}

// HasStopBlockers reports whether anything in blockers should stop a
// conclusion.
func HasStopBlockers(blockers Blockers) bool {
	return len(blockers.Quarantined) > 0 || len(blockers.Contradictions) > 0
}

// BlockerHead and BlockerFoot wrap the lines DescribeStopBlockers produces.
const (
	BlockerHead = "⚠ Do not conclude yet — the room has ruled on context your answer may rest on:"
	BlockerFoot = "Address these before concluding: drop or correct anything that rested on quarantined context, and " +
		"take a position on the contradiction with corroborate_claim or contest_claim. If your answer genuinely " +
		"does not depend on them, say so explicitly and you will be allowed to stop."
)

// DescribeStopBlockers renders the lines a Stop hook writes for blockers.
// Says what is wrong and what would resolve it — a refusal that does not
// name its own exit is a wall. max <= 0 defaults to 6.
func DescribeStopBlockers(blockers Blockers, max int) []string {
	if max <= 0 {
		max = 6
	}
	var lines []string
	for _, f := range blockers.Quarantined {
		cited := ""
		if len(f.CitedByClaimSeqs) > 0 {
			plural := ""
			if len(f.CitedByClaimSeqs) > 1 {
				plural = "s"
			}
			parts := make([]string, len(f.CitedByClaimSeqs))
			for i, s := range f.CitedByClaimSeqs {
				parts[i] = strconv.Itoa(s)
			}
			cited = fmt.Sprintf(" (cited by your claim%s #%s)", plural, strings.Join(parts, ", #"))
		}
		why := ""
		if f.Reason != "" {
			why = fmt.Sprintf(": \"%s\"", oneLine(f.Reason, 100))
		}
		kind := f.TargetKind
		if kind == "" {
			kind = "item"
		}
		relation := f.Relation
		if relation == "" {
			relation = "used"
		}
		lines = append(lines, fmt.Sprintf("• seq %d — the room QUARANTINED this %s you %s%s%s",
			f.TargetSeq, kind, relation, cited, why))
	}
	for _, v := range blockers.Contradictions {
		var against []string
		if v.Shortfall != nil && v.Shortfall.Missing != nil {
			for _, s := range v.Shortfall.Missing.Contradiction {
				against = append(against, fmt.Sprintf("#%d", s))
			}
		}
		claimSeq := 0
		if v.ClaimSeq != nil {
			claimSeq = *v.ClaimSeq
		}
		lines = append(lines, fmt.Sprintf(`• claim #%d "%s" contradicts admitted claim(s) %s and is still awaiting your position`,
			claimSeq, oneLine(v.Statement, 120), strings.Join(against, ", ")))
	}

	if len(lines) <= max {
		return lines
	}
	shown := append([]string{}, lines[:max]...)
	shown = append(shown, fmt.Sprintf("+%d more — call get_updates for the rest.", len(lines)-max))
	return shown
}

// oneLine trims an untrusted statement to one bounded line, collapsing
// internal whitespace runs to a single space and trimming the ends — the Go
// analogue of oneLine(s, max) in attention.mjs.
func oneLine(s string, max int) string {
	flat := strings.Join(strings.Fields(s), " ")
	r := []rune(flat)
	if len(r) <= max {
		return flat
	}
	return string(r[:max-1]) + "…"
}
