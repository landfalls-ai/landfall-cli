package narrate

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/landfalls-ai/landfall-cli/internal/client"
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
// The projection's WIRE TYPES (client.Attention, client.VoteAwaited,
// client.FlaggedContext, client.Divergence, …) belong to internal/client, the
// layer that unmarshals them. This file renders them and owns nothing about
// their shape.
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
// projection — used to refresh on arrival rather than on a timer. Takes the
// event's TYPE rather than the event, so the one implementation serves both
// the tool-call path and the live-socket path without either of them having
// to hold a particular event struct.
func TouchesAttention(eventType string) bool {
	for _, p := range AttentionPrefixes {
		if strings.HasPrefix(eventType, p) {
			return true
		}
	}
	return false
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
func VoteKey(v client.VoteAwaited) string {
	fresh := "fresh"
	if v.Stale {
		fresh = "stale"
	}
	return seqOrUndefined(v.ClaimSeq) + ":" + fresh
}

// VoteRequestBlock builds the piggyback block for a tool result.
//
// notified is the set of keys already announced to this session; it is read
// here, never mutated — the caller decides when to record the returned Keys
// as notified, so a caller that fails to deliver the block does not lose the
// announcement. max <= 0 defaults to VoteLinesMax.
//
// attention may be nil (an unreachable server means "nothing known to be
// waiting"), matching the source's own `attention?.votesAwaited` guard.
// Returns a zero-value Block (Text == "") when there is nothing new to say.
//
// It closes with VoteRequestFoot, the line for a surface that registers
// corroborate_claim and contest_claim; VoteRequestBlockFor picks the line for
// one that does not.
func VoteRequestBlock(attention *client.Attention, notified map[string]bool, max int) Block {
	return VoteRequestBlockFor(attention, notified, max, true)
}

// VoteRequestFoot and VoteRequestFootBridge close a vote-request block.
//
// A vote request is room news addressed to the agent, and room news is the
// person's call: the agent says so in one line and takes a position only when
// the person asks, the same rule its standing instructions give for any ask
// from the room. The block used to end "vote and carry on", which told the
// agent to post to the room on its own.
//
// The bridge line names no verb. With the bridge running, corroborate_claim
// and contest_claim are not registered (the worker never votes, see
// bridge/vetting.go), so naming them sends the agent looking for a verb it does
// not have, or "answering" through share_with_room.
const (
	VoteRequestFoot = "Tell the person in one line; take a position with corroborate_claim or contest_claim only if they ask you to. " +
		"A position is never a decision."
	VoteRequestFootBridge = "Tell the person in one line; whether to take a position is their call, and they can take it in the war room."
)

// VoteRequestBlockFor is VoteRequestBlock for a surface that can, or cannot,
// vote: canVote false closes with VoteRequestFootBridge.
func VoteRequestBlockFor(attention *client.Attention, notified map[string]bool, max int, canVote bool) Block {
	if attention == nil {
		return Block{}
	}
	if max <= 0 {
		max = VoteLinesMax
	}
	// Already sorted by urgency server-side; keep that order rather than
	// imposing a second one that could disagree with it.
	var fresh []client.VoteAwaited
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
	if canVote {
		lines = append(lines, VoteRequestFoot)
	} else {
		lines = append(lines, VoteRequestFootBridge)
	}

	keys := make([]string, len(shown))
	for i, v := range shown {
		keys[i] = VoteKey(v)
	}
	return Block{Text: strings.Join(lines, "\n"), Keys: keys}
}

// DivergenceKey is the key a divergence nudge is remembered by, so the same
// established/observed pair is not re-announced on every tool call.
func DivergenceKey(d client.Divergence) string {
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
func DivergenceBlock(divergence *client.Divergence, notified map[string]bool) Block {
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
	Quarantined    []client.FlaggedContext
	Contradictions []client.VoteAwaited
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
//
// attention may be nil, matching the source's `attention?.…` guards.
func StopBlockers(attention *client.Attention) Blockers {
	if attention == nil {
		return Blockers{}
	}
	var quarantined []client.FlaggedContext
	for _, f := range attention.FlaggedOwnContext {
		if f.State == "quarantined" {
			quarantined = append(quarantined, f)
		}
	}
	var contradictions []client.VoteAwaited
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
				parts[i] = strconv.FormatInt(s, 10)
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
		lines = append(lines, fmt.Sprintf("• seq %s — the room QUARANTINED this %s you %s%s%s",
			seqOrUndefined(f.TargetSeq), kind, relation, cited, why))
	}
	for _, v := range blockers.Contradictions {
		var against []string
		if v.Shortfall != nil && v.Shortfall.Missing != nil {
			for _, s := range v.Shortfall.Missing.Contradiction {
				against = append(against, fmt.Sprintf("#%d", s))
			}
		}
		lines = append(lines, fmt.Sprintf(`• claim #%s "%s" contradicts admitted claim(s) %s and is still awaiting your position`,
			seqOrUndefined(v.ClaimSeq), oneLine(v.Statement, 120), strings.Join(against, ", ")))
	}

	if len(lines) <= max {
		return lines
	}
	shown := append([]string{}, lines[:max]...)
	shown = append(shown, fmt.Sprintf("+%d more — call get_updates for the rest.", len(lines)-max))
	return shown
}

// seqOrUndefined renders a wire seq the way a JS template interpolation of it
// would: the number, or the literal "undefined" when the server omitted it.
// The wire types carry these as pointers precisely because absent and zero
// are different things — seq 0 is a real event — so a missing seq must never
// print as "#0" and read like a position on the incident's first event.
func seqOrUndefined(seq *int64) string {
	if seq == nil {
		return "undefined"
	}
	return strconv.FormatInt(*seq, 10)
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

// Vote is one staged claim awaiting the PERSON's position, as `landfall
// watch` carries it for a front end to ask about (contracts/cli-json.md §1).
// Built from the human view of the attention projection (no agent instance,
// review finding 2), enriched from the claims projection where it says more.
type Vote struct {
	ClaimSeq      int64  `json:"claimSeq"`
	Class         string `json:"class,omitempty"`
	Statement     string `json:"statement"`
	AuthoredBy    string `json:"authoredBy,omitempty"`
	AuthorIsAgent bool   `json:"authorIsAgent"`
	// AuthorHuman is the person behind an agent author, when known.
	AuthorHuman    string `json:"authorHuman,omitempty"`
	PositionsSoFar int    `json:"positionsSoFar"`
	// Needed is the positions the bar asks for, when the server says.
	Needed *int `json:"needed,omitempty"`
	// Shortfall is how many more positions it still needs, when that is what
	// it is short of.
	Shortfall   *int   `json:"shortfall,omitempty"`
	ExpiresInMs *int64 `json:"expiresInMs,omitempty"`
	Stale       bool   `json:"stale"`
	// Evidence is the claim's references in short words, when it has any.
	Evidence string `json:"evidence,omitempty"`
	// Mine is true when the person or their agent authored it; a front end
	// never offers those.
	Mine bool `json:"mine"`
}

// VotesMax bounds the list one watch line carries.
const VotesMax = 20

// VotesOf turns the person's attention into the watch's vote list, in the
// server's urgency order. claims (may be nil) adds the bar, the evidence and
// the author's identity; parts names the human behind an agent author; me is
// the person's humanActorId; elapsedMs is how long ago the attention was read,
// so expiresInMs counts down between reads instead of standing still.
func VotesOf(att *client.Attention, claims *client.ClaimsProjection, parts []client.Participant, me string, elapsedMs float64) []Vote {
	if att == nil {
		return nil
	}
	bySeq := map[int64]client.ClaimView{}
	if claims != nil {
		for _, c := range claims.Claims {
			bySeq[c.Seq] = c
		}
	}
	names := humanNames(parts)
	var out []Vote
	for _, v := range att.VotesAwaited {
		if v.ClaimSeq == nil {
			continue
		}
		vote := Vote{
			ClaimSeq:       *v.ClaimSeq,
			Class:          oneLine(Printable(v.Class), 40),
			Statement:      oneLine(Printable(v.Statement), 300),
			AuthoredBy:     oneLine(Printable(v.AuthoredBy), 60),
			AuthorIsAgent:  v.AuthorIsAgent,
			PositionsSoFar: v.PositionsSoFar,
			Stale:          v.Stale,
		}
		if v.ExpiresInMs != nil {
			left := int64(math.Round(*v.ExpiresInMs - elapsedMs))
			vote.ExpiresInMs = &left
			if left <= 0 {
				vote.Stale = true
			}
		}
		if v.Shortfall != nil && v.Shortfall.Missing != nil && v.Shortfall.Missing.Corroborators != nil {
			n := *v.Shortfall.Missing.Corroborators
			vote.Shortfall = &n
		}
		if c, ok := bySeq[*v.ClaimSeq]; ok {
			if c.Outcome != nil && c.Outcome.RequiredBar != nil && c.Outcome.RequiredBar.Corroborators > 0 {
				n := c.Outcome.RequiredBar.Corroborators
				vote.Needed = &n
			}
			vote.Evidence = evidenceOf(c.Provenance)
			if me != "" && c.Author.HumanActorID == me {
				vote.Mine = true
			}
			if v.AuthorIsAgent && c.Author.HumanActorID != "" {
				vote.AuthorHuman = names[c.Author.HumanActorID]
			}
		}
		if vote.Needed == nil && vote.Shortfall != nil {
			n := vote.PositionsSoFar + *vote.Shortfall
			vote.Needed = &n
		}
		out = append(out, vote)
		if len(out) == VotesMax {
			break
		}
	}
	return out
}

// evidenceOf is a claim's references in one short line: each quote, or the
// kind and seq of a reference that carries no quote.
func evidenceOf(refs []client.ClaimProvenance) string {
	var parts []string
	for _, r := range refs {
		q := oneLine(Printable(r.Quote), 80)
		if q == "" {
			q = oneLine(Printable(r.SourceType), 20)
			if r.SourceSeq != nil {
				q = strings.TrimSpace(q + " #" + strconv.FormatInt(*r.SourceSeq, 10))
			}
		}
		if q != "" {
			parts = append(parts, q)
		}
	}
	return oneLine(strings.Join(parts, " · "), 160)
}

// humanNames is each person's display name by humanActorId, preferring the
// name their browser tab carries (an agent's is a fallback), as peopleOf does.
func humanNames(parts []client.Participant) map[string]string {
	out := map[string]string{}
	for _, p := range parts {
		if p.HumanActorID == "" {
			continue
		}
		name := oneLine(Printable(p.DisplayName), 60)
		if name == "" {
			continue
		}
		tab := strings.HasPrefix(p.AgentInstanceID, "web:") || (p.AgentInstanceID == "" && p.EdgeAgentLabel == "")
		if _, ok := out[p.HumanActorID]; !ok || tab {
			out[p.HumanActorID] = name
		}
	}
	return out
}
