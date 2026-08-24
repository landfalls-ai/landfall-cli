package bridge

import (
	"context"
	"fmt"
	"strings"
)

// narrateHandOff is what the room sees while the worker is publishing on the
// responder's behalf.
//
// WHY THIS EXISTS AT ALL. The main agent's `record_activity` moves to the
// worker with the rest of the publish surface, and presence would otherwise go
// half-dark: serve's 15s heartbeat keeps the responder's tile ALIVE, but only
// with a generic "investigating". The descriptive per-action line — the thing
// that tells other investigators what this person is actually doing right now —
// comes from narration, and if the worker does not take that over, the room
// gets a participant that acts but never says anything.
//
// That is why record_activity stays on the main agent until this exists. The
// senior review caught the ordering: removing it first would have shipped an
// MVP with a live-but-mute participant.
//
// KEPT SHORT AND FACTUAL. This lands in a feed other people read under
// pressure. It says what is happening, not how the machinery works — "sharing a
// finding", never "draining spool entry 3f2a".
func narrateHandOff(kind Kind, text string) string {
	switch kind {
	case KindClaim:
		return "staging a claim for the room to vet"
	case KindWidget:
		return "adding a widget to the dashboard"
	case KindNote:
		return "sharing a note"
	default:
		if subject := firstClause(text); subject != "" {
			return fmt.Sprintf("sharing a finding: %s", subject)
		}
		return "sharing a finding"
	}
}

// firstClause is a short, safe excerpt for the activity line.
//
// Bounded hard at 60 runes. This is a presence line in a shared feed, not the
// finding itself — the full text arrives moments later as the published item,
// so there is nothing to gain by making the preview long and something to lose
// (a wall of text in everyone's participant strip).
//
// Runes, not bytes: truncating mid-codepoint would render as a replacement
// character in every viewer.
func firstClause(text string) string {
	t := strings.TrimSpace(text)
	if t == "" {
		return ""
	}
	// Stop at the first sentence boundary when there is one close by, so the
	// excerpt reads as a complete thought rather than a severed one.
	if i := strings.IndexAny(t, ".\n"); i > 0 && i <= 60 {
		return strings.TrimSpace(t[:i])
	}

	r := []rune(t)
	if len(r) <= 60 {
		return t
	}
	return strings.TrimSpace(string(r[:60])) + "…"
}

// narrate posts one activity line, best-effort.
//
// Failures are swallowed on purpose: narration is a courtesy to the room, and a
// heartbeat that could not be delivered must never stop the publish that
// follows it. The responder's finding reaching the room matters; the line
// saying it is about to does not.
func (w *Worker) narrate(ctx context.Context, pub Publisher, doing string) {
	_ = pub.Heartbeat(ctx, doing)
}
