package daemon

import (
	"context"
	"strings"

	"github.com/landfalls-ai/landfall-cli/internal/spool"
)

// HoldStore is the spool as the hold needs it (Phase US3). The daemon accepts
// shares only when one is configured; until then `share` is refused and the
// front end keeps its own spool path.
type HoldStore interface {
	AcceptKind(incidentID, agentInstanceID, text string, refs []string, widget *spool.WidgetPayload, sourceQueryFailed bool, kind string) (*spool.Entry, error)
	Hold(incidentID, id string, matched []string) error
	Held(incidentID string) ([]*spool.Entry, error)
	ReleaseHeld(incidentID string) (int, error)
	DropHeld(incidentID, id string) error
}

// Match reports which fingerprints a share's text names. Deliberately simple
// and explainable (data-model "WorkspaceFingerprints"): a commit hash anywhere,
// a tracked path longer than eight characters anywhere, a name as a whole word.
func (fp *Fingerprints) Match(text string) []string {
	if fp == nil || text == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, c := range fp.Commits {
		if len(c) >= 7 && strings.Contains(text, c) {
			add(c)
		}
	}
	for _, p := range fp.Paths {
		if len(p) > 8 && strings.Contains(text, p) {
			add(p)
		}
	}
	lower := strings.ToLower(text)
	for _, n := range fp.Names {
		if n == "" {
			continue
		}
		if containsWord(lower, strings.ToLower(n)) {
			add(n)
		}
	}
	return out
}

func containsWord(haystack, word string) bool {
	idx := 0
	for {
		i := strings.Index(haystack[idx:], word)
		if i < 0 {
			return false
		}
		start := idx + i
		end := start + len(word)
		before := start == 0 || !isWordByte(haystack[start-1])
		after := end == len(haystack) || !isWordByte(haystack[end])
		if before && after {
			return true
		}
		idx = start + 1
	}
}

func isWordByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// heldCount is what the status line shows as "M held".
func (d *Daemon) heldCount(roomKey string) int {
	if d.opts.Hold == nil {
		return 0
	}
	room := d.room(roomKey)
	if room == nil {
		return 0
	}
	held, err := d.opts.Hold.Held(room.Config.IncidentID)
	if err != nil {
		return 0
	}
	return len(held)
}

// handleHold answers share / held / allow-cwd / drop-held.
func (d *Daemon) handleHold(_ context.Context, req Request) Response {
	if d.opts.Hold == nil {
		return fail("this daemon does not hold shares; the front end publishes directly")
	}
	room := d.room(req.RoomKey)
	if room == nil {
		return fail("no such room")
	}
	inc := room.Config.IncidentID
	switch req.Op {
	case "share":
		rd, okr := room.Reader(req.ReaderName)
		if !okr || rd.Kind != KindAgent {
			return fail("share is an agent reader's verb")
		}
		var widget *spool.WidgetPayload
		if req.Widget != nil {
			widget = &spool.WidgetPayload{}
			if t, ok := req.Widget["widgetType"].(string); ok {
				widget.WidgetType = t
			}
			if t, ok := req.Widget["title"].(string); ok {
				widget.Title = t
			}
			if data, ok := req.Widget["data"].(map[string]any); ok {
				widget.Data = data
			}
		}
		e, err := d.opts.Hold.AcceptKind(inc, room.AgentInstanceID, req.Text, req.Refs, widget, req.SourceQueryFailed, req.Kind)
		if err != nil {
			return fail("could not record the hand-off: " + err.Error())
		}
		res := ok()
		res.EntryID, res.Redacted = e.ID, e.Redacted
		d.mu.Lock()
		fp := d.fingerprints[room.Key]
		allowed := room.CwdAllowed
		d.mu.Unlock()
		if !allowed {
			if matched := fp.Match(e.Text); len(matched) > 0 {
				if herr := d.opts.Hold.Hold(inc, e.ID, matched); herr == nil {
					res.Held, res.Matched = true, matched
				}
			}
		}
		return res
	case "held":
		items, err := d.opts.Hold.Held(inc)
		if err != nil {
			return fail(err.Error())
		}
		res := ok()
		res.Held = len(items) > 0
		for _, e := range items {
			line := e.ID + " " + strings.Join(e.Held.Matched, ", ")
			res.Matched = append(res.Matched, line)
		}
		return res
	case "allow-cwd":
		room.mu.Lock()
		room.CwdAllowed = true
		room.mu.Unlock()
		n, err := d.opts.Hold.ReleaseHeld(inc)
		if err != nil {
			return fail(err.Error())
		}
		d.save()
		res := ok()
		res.Released = n
		return res
	case "drop-held":
		if err := d.opts.Hold.DropHeld(inc, req.EntryID); err != nil {
			return fail(err.Error())
		}
		return ok()
	}
	return fail("unknown hold op")
}
