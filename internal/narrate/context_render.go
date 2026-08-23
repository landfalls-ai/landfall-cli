package narrate

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Ported from src/context-render.mjs — pure rendering for the war-room
// context frame (feature 116, cross-repo-followup.md). Three render
// functions, one per server read: RenderFrame (GET .../edge/context/frame),
// RenderDelta (GET .../edge/context/delta), RenderSearchHits (GET
// .../edge/context/search). No I/O, no clock — same testable-without-a-
// network discipline as narrate.go/attention.go. Lives in this same package
// per plan.md's Project Structure: it is the same kind of pure rendering
// logic as the rest of this package.

// Incident is the incident summary embedded in a Frame.
type Incident struct {
	Title       string
	Severity    string
	Status      string
	AlertSource string
}

// BriefItem is one established/open item in a Brief.
type BriefItem struct {
	Seq       int
	Statement string
	By        string
}

// Brief is the established/working-theory/open sections of a Frame.
type Brief struct {
	Established   []BriefItem
	WorkingTheory []BriefItem
	Open          []BriefItem
}

// Participant is one war-room participant listed in a Frame.
type Participant struct {
	DisplayName    string
	EdgeAgentLabel string
	// Active is active/away/unknown — nil means unknown, exactly like
	// `active === undefined` on the wire when the server's presence store
	// could not be read. Never fabricated as either known state.
	Active *bool
}

// ContextFrame (data-model.md) is the full room context brief join_war_room/
// get_brief render.
type ContextFrame struct {
	Incident     Incident
	Brief        Brief
	Participants []Participant
	// FreshnessMs is nil when the frame carries no freshness figure at all
	// (renders as "freshness unknown"), matching
	// `typeof frame.freshnessMs === 'number'` in the source.
	FreshnessMs *float64
	// AsOfSeq is nil when absent (renders as "?"), matching
	// `frame.asOfSeq ?? '?'` in the source.
	AsOfSeq *int
}

func presenceWord(active *bool) string {
	if active == nil {
		return "unknown"
	}
	if *active {
		return "active"
	}
	return "away"
}

func participantLabel(p Participant) string {
	name := p.DisplayName
	if name == "" {
		name = "Participant"
	}
	label := ""
	if p.EdgeAgentLabel != "" {
		label = " · " + p.EdgeAgentLabel
	}
	return fmt.Sprintf("%s%s (%s)", name, label, presenceWord(p.Active))
}

func briefLines(items []BriefItem, heading string) []string {
	if len(items) == 0 {
		return nil
	}
	lines := make([]string, 0, len(items)+1)
	lines = append(lines, heading+":")
	for _, it := range items {
		lines = append(lines, fmt.Sprintf("  #%d %s — %s", it.Seq, it.Statement, it.By))
	}
	return lines
}

// RenderFrame renders a ContextFrame as the text join_war_room/get_brief
// return — a real, usable brief in one call (SC-002).
//
// frame may be nil (renders "No incident context available.") and any of
// its fields may be their zero value (a genuinely partial frame) — this
// never panics on a missing field, matching the source's own
// `if (!frame || typeof frame !== 'object') return '...'` guard plus its
// liberal use of `?? {}`/optional chaining on every nested field.
func RenderFrame(frame *ContextFrame) string {
	if frame == nil {
		return "No incident context available."
	}

	title := frame.Incident.Title
	if title == "" {
		title = "(untitled incident)"
	}
	headParts := []string{title}
	if frame.Incident.Severity != "" {
		headParts = append(headParts, frame.Incident.Severity)
	}
	if frame.Incident.Status != "" {
		headParts = append(headParts, frame.Incident.Status)
	}
	lines := []string{strings.Join(headParts, " · ")}
	if frame.Incident.AlertSource != "" {
		lines = append(lines, "Alert source: "+frame.Incident.AlertSource)
	}

	established := briefLines(frame.Brief.Established, "Established")
	openItems := make([]BriefItem, 0, len(frame.Brief.WorkingTheory)+len(frame.Brief.Open))
	openItems = append(openItems, frame.Brief.WorkingTheory...)
	openItems = append(openItems, frame.Brief.Open...)
	open := briefLines(openItems, "Open")
	if len(established) > 0 || len(open) > 0 {
		lines = append(lines, "")
		lines = append(lines, established...)
		if len(established) > 0 && len(open) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, open...)
	} else {
		lines = append(lines, "", "No findings or open items yet — this is a genuinely fresh incident.")
	}

	if len(frame.Participants) > 0 {
		parts := make([]string, len(frame.Participants))
		for i, p := range frame.Participants {
			parts[i] = participantLabel(p)
		}
		lines = append(lines, "", "Participants: "+strings.Join(parts, ", "))
	}

	freshness := "freshness unknown"
	if frame.FreshnessMs != nil {
		secs := math.Round(*frame.FreshnessMs / 1000)
		if secs < 0 {
			secs = 0
		}
		freshness = fmt.Sprintf("%ds stale", int64(secs))
	}
	asOfSeq := "?"
	if frame.AsOfSeq != nil {
		asOfSeq = strconv.Itoa(*frame.AsOfSeq)
	}
	lines = append(lines, "", fmt.Sprintf("As of seq %s (%s).", asOfSeq, freshness))
	return strings.Join(lines, "\n")
}

// FrameCursor returns the seq to resume delivery from — the frame's own
// cursor, so a caller does not need to separately track "the highest seq in
// the brief". Returns nil for a nil frame or an absent AsOfSeq.
func FrameCursor(frame *ContextFrame) *int {
	if frame == nil {
		return nil
	}
	return frame.AsOfSeq
}

// DeltaItem is one addressed/substantive item in a FrameDelta.
type DeltaItem struct {
	Seq     int
	Type    string
	Class   string // e.g. "addressed"
	By      string
	Summary string
}

// FrameDelta (data-model.md) is what RenderDelta renders.
type FrameDelta struct {
	Items        []DeltaItem
	RoutineCount int
}

// RenderDelta renders a FrameDelta — addressed/substantive items in full,
// routine activity as a count only (FR-007/FR-008). Returns "" when there is
// nothing owed (delta is nil, or has neither items nor a routine count), so
// a caller can drop it from a result untouched rather than emitting an
// empty/zero-item block.
func RenderDelta(delta *FrameDelta) string {
	if delta == nil || (len(delta.Items) == 0 && delta.RoutineCount == 0) {
		return ""
	}
	lines := []string{fmt.Sprintf("⚠ %d update(s) from other investigators since your last check:", len(delta.Items))}
	for _, it := range delta.Items {
		tag := "•"
		if it.Class == "addressed" {
			tag = "➤"
		}
		who := ""
		if it.By != "" {
			who = fmt.Sprintf(" [%s]", it.By)
		}
		summary := ""
		if it.Summary != "" {
			summary = " — " + it.Summary
		}
		lines = append(lines, fmt.Sprintf("%s #%d %s%s%s", tag, it.Seq, it.Type, who, summary))
	}
	if delta.RoutineCount > 0 {
		lines = append(lines, fmt.Sprintf("(+%d routine update(s) — counted, not shown)", delta.RoutineCount))
	}
	return strings.Join(lines, "\n")
}

// SearchHit is one matching event in a SearchResult.
type SearchHit struct {
	Seq     int
	Type    string
	By      string
	Snippet string
}

// SearchResult is what edge/context/search returns.
type SearchResult struct {
	Hits []SearchHit
}

// RenderSearchHits renders real search hits (FR-005/SC-006) — never a bare
// count. result may be nil (renders the zero-hits line for query).
func RenderSearchHits(result *SearchResult, query string) string {
	var hits []SearchHit
	if result != nil {
		hits = result.Hits
	}
	if len(hits) == 0 {
		return fmt.Sprintf("0 matching event(s) for \"%s\".", query)
	}
	lines := []string{fmt.Sprintf("%d matching event(s) for \"%s\":", len(hits), query)}
	for _, h := range hits {
		lines = append(lines, fmt.Sprintf("#%d %s [%s] — %s", h.Seq, h.Type, h.By, h.Snippet))
	}
	return strings.Join(lines, "\n")
}
