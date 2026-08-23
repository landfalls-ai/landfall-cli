package narrate

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

// Ported from src/context-render.mjs — pure rendering for the war-room
// context frame (feature 116, cross-repo-followup.md). Three render
// functions, one per server read: RenderFrame (GET .../edge/context/frame),
// RenderDelta (GET .../edge/context/delta), RenderSearchHits (GET
// .../edge/context/search). No I/O, no clock — same testable-without-a-
// network discipline as narrate.go/attention.go. Lives in this same package
// per plan.md's Project Structure: it is the same kind of pure rendering
// logic as the rest of this package.
//
// The WIRE TYPES these render (ContextFrame, FrameDelta, SearchResult and
// their members) belong to internal/client, which is the layer that actually
// unmarshals the server's JSON. This package consumes them rather than
// declaring a second, drifting set of its own — a rendering package is not
// the owner of a wire contract. The dependency is one-way by construction:
// internal/client has no rendering logic and never imports this package.

func presenceWord(active *bool) string {
	if active == nil {
		return "unknown"
	}
	if *active {
		return "active"
	}
	return "away"
}

func participantLabel(p client.Participant) string {
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

func briefLines(items []client.BriefItem, heading string) []string {
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

// RenderFrame renders a client.ContextFrame as the text join_war_room/
// get_brief return — a real, usable brief in one call (SC-002).
//
// frame may be nil (renders "No incident context available.") and any of
// its fields may be their zero value (a genuinely partial frame) — this
// never panics on a missing field, matching the source's own
// `if (!frame || typeof frame !== 'object') return '...'` guard plus its
// liberal use of `?? {}`/optional chaining on every nested field.
func RenderFrame(frame *client.ContextFrame) string {
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

	// `disproved` is deliberately NOT rendered: the source composes "Open"
	// from workingTheory + open only, and a disproved item resurfacing under
	// an "Open" heading would read as still-live.
	established := briefLines(frame.Brief.Established, "Established")
	openItems := make([]client.BriefItem, 0, len(frame.Brief.WorkingTheory)+len(frame.Brief.Open))
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
		asOfSeq = strconv.FormatInt(*frame.AsOfSeq, 10)
	}
	lines = append(lines, "", fmt.Sprintf("As of seq %s (%s).", asOfSeq, freshness))
	return strings.Join(lines, "\n")
}

// NoCursor is what FrameCursor returns when a frame carries no cursor at all
// — the same value session.Session's own cursor starts at, so handing it
// straight to AdvanceCursorTo is a no-op rather than a rewind. It is
// deliberately NOT a new sentinel: the Node original returns `undefined` and
// its caller skips the advance, which is exactly what -1 achieves against a
// monotonic cursor that already begins at -1.
const NoCursor int64 = -1

// FrameCursor returns the seq to resume delivery from — the frame's own
// cursor, so a caller does not need to separately track "the highest seq in
// the brief". Returns NoCursor for a nil frame or an absent AsOfSeq.
func FrameCursor(frame *client.ContextFrame) int64 {
	if frame == nil || frame.AsOfSeq == nil {
		return NoCursor
	}
	return *frame.AsOfSeq
}

// RenderDelta renders a client.FrameDelta — addressed/substantive items in
// full, routine activity as a count only (FR-007/FR-008). Returns "" when
// there is nothing owed (delta is nil, or has neither items nor a routine
// count), so a caller can drop it from a result untouched rather than
// emitting an empty/zero-item block.
func RenderDelta(delta *client.FrameDelta) string {
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

// RenderSearchHits renders real search hits (FR-005/SC-006) — never a bare
// count. result may be nil (renders the zero-hits line for query).
func RenderSearchHits(result *client.SearchResult, query string) string {
	var hits []client.SearchHit
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
