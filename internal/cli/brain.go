package cli

// brain.go — `landfall brain --room <k> [--q "<text>"]` (contract §9, review
// #10): what the company second brain holds that bears on this room.
//
//   - No --q: GET …/incidents/:id/memory with the room's own session through
//     the daemon. The server picks the entries relevant to the incident's
//     title and severity (the Edge Bridge's history read).
//   - With --q: GET /o/:slug/memory/entries with the person's own token (an
//     edge session cannot reach org-level routes), matched here word by word
//     against each entry's title, summary, subject and tags.
//
// Only fields an entry really carries are mapped (title, summary, confidence,
// the incidents it came from, its kind and maturity where present). There is
// no "fix" or "why" field on an entry, so none is invented.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/hooks"
	"github.com/spf13/cobra"
)

const (
	brainMatchesMax = 10
	brainSummaryMax = 400
	msgBrainMissing = "The company second brain is not available to the CLI yet."
)

func newBrainCommand(ui *UI) *cobra.Command {
	var q string
	c := newReadCommand(ui, "brain", func(cmd *cobra.Command, ws hooks.Workspace, room string) map[string]any {
		return RunBrain(cmdContext(cmd), room, q, defaultReadDeps(ws))
	})
	c.Flags().StringVar(&q, "q", "", "words to look for")
	return c
}

// RunBrain builds the brain answer.
func RunBrain(ctx context.Context, roomSel, q string, d ReadDeps) map[string]any {
	q = strings.TrimSpace(q)
	if q == "" {
		room, why := pickRoom(d, roomSel)
		if why != "" {
			return failure(why)
		}
		raw, err := d.RoomRead(ctx, room.RoomKey, "/memory")
		if err != nil {
			var he *client.HTTPError
			if errors.As(err, &he) && (he.Status == http.StatusNotFound || he.Status == http.StatusForbidden) {
				return failure(msgBrainMissing)
			}
			return failure(roomRefusal(err, "the company second brain"))
		}
		var body struct {
			Entries []map[string]any `json:"entries"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return failure("Landfall sent second brain entries this CLI could not read.")
		}
		matches := make([]map[string]any, 0, len(body.Entries))
		for _, e := range body.Entries {
			if len(matches) == brainMatchesMax {
				break
			}
			var incidents []string
			for _, id := range jList(e, "sourceIncidentIds") {
				if s, ok := id.(string); ok && s != "" {
					incidents = append(incidents, s)
				}
			}
			matches = append(matches, brainMatch(e, incidents))
		}
		return map[string]any{"ok": true, "matches": matches}
	}

	org, err := d.Org(ctx)
	if err != nil || org == nil {
		return failure("Sign in to search the company second brain: run landfall login.")
	}
	raw, err := org.MemoryEntries(ctx)
	if err != nil {
		var he *client.HTTPError
		if errors.As(err, &he) && (he.Status == http.StatusNotFound || he.Status == http.StatusForbidden) {
			return failure(msgBrainMissing)
		}
		return failure(personRefusal(err, "the company second brain"))
	}
	var body struct {
		Entries []map[string]any `json:"entries"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return failure("Landfall sent second brain entries this CLI could not read.")
	}
	words := queryWords(q)
	type scored struct {
		entry map[string]any
		score int
	}
	var hits []scored
	for _, e := range body.Entries {
		if jStr(e, "status") == "deleted" {
			continue
		}
		if score := entryScore(e, words); score > 0 {
			hits = append(hits, scored{e, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	matches := make([]map[string]any, 0, brainMatchesMax)
	for _, h := range hits {
		if len(matches) == brainMatchesMax {
			break
		}
		var incidents []string
		for _, si := range jList(h.entry, "sourceIncidents") {
			if m, ok := si.(map[string]any); ok {
				if id := jStr(m, "incidentDisplayId"); id != "" {
					incidents = append(incidents, id)
				}
			}
		}
		matches = append(matches, brainMatch(h.entry, incidents))
	}
	return map[string]any{"ok": true, "matches": matches}
}

// brainMatch maps one entry to the contract, real fields only.
func brainMatch(e map[string]any, incidents []string) map[string]any {
	out := map[string]any{"title": oneLineText(jStr(e, "title"), 160)}
	if s := jStr(e, "summary"); s != "" {
		out["summary"] = oneLineText(s, brainSummaryMax)
	}
	if c := jStr(e, "confidence"); c != "" {
		out["confidence"] = c
	}
	if len(incidents) > 0 {
		out["incident"] = incidents[0]
		out["incidents"] = incidents
	}
	for _, key := range []string{"kind", "maturity", "id"} {
		if v := jStr(e, key); v != "" {
			out[key] = v
		}
	}
	// conflicted or downgraded: worth saying beside the entry.
	if s := jStr(e, "status"); s != "" && s != "active" {
		out["status"] = s
	}
	return out
}

// queryWords are the lowercase words of a query worth matching.
func queryWords(q string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r > 127)
	}) {
		if len([]rune(w)) >= 2 {
			out = append(out, w)
		}
	}
	return out
}

// entryScore counts query words an entry mentions; a title hit counts twice.
func entryScore(e map[string]any, words []string) int {
	title := strings.ToLower(jStr(e, "title"))
	rest := strings.ToLower(jStr(e, "summary") + " " + jStr(e, "subjectKey"))
	for _, t := range jList(e, "tags") {
		if s, ok := t.(string); ok {
			rest += " " + strings.ToLower(s)
		}
	}
	score := 0
	for _, w := range words {
		if strings.Contains(title, w) {
			score += 2
		} else if strings.Contains(rest, w) {
			score++
		}
	}
	return score
}
