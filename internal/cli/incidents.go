package cli

// incidents.go — `landfall incidents [--all]`: the organization's open
// incidents for the Claude Code mod's switchboard (contract §3, review #6).
//
// One list read with the person's own token (an edge token is refused on the
// org-level route), keyset-paged, at most incidentPagesMax pages. Status is
// filtered here: the route takes only limit and cursor. Presence and Beacon's
// state are NOT read per incident: that is the N+1 burst that trips the
// deployed per-IP rate limit. They come from the room daemon, and only for
// rooms this checkout is in, where it already holds them.

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/daemon"
	"github.com/landfalls-ai/landfall-cli/internal/hooks"
	"github.com/spf13/cobra"
)

const (
	// incidentPageSize is the route's maximum page.
	incidentPageSize = 200
	// incidentPagesMax bounds the list to two sequential reads.
	incidentPagesMax = 2
	// recentlyResolved is how far back --all reaches for resolved incidents.
	recentlyResolved = 24 * time.Hour
)

func newIncidentsCommand(ui *UI) *cobra.Command {
	var all bool
	c := newReadCommand(ui, "incidents", func(cmd *cobra.Command, ws hooks.Workspace, _ string) map[string]any {
		return RunIncidents(cmdContext(cmd), all, defaultReadDeps(ws))
	})
	c.Flags().BoolVar(&all, "all", false, "also list incidents resolved in the last 24 hours")
	return c
}

// closedStatus reports a status that means the incident is over.
func closedStatus(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "resolved", "closed", "postmortem":
		return true
	}
	return false
}

// RunIncidents builds the switchboard answer.
func RunIncidents(ctx context.Context, all bool, d ReadDeps) map[string]any {
	org, err := d.Org(ctx)
	if err != nil || org == nil {
		return failure("Sign in to list incidents: run landfall login.")
	}

	now := d.now()
	type row struct {
		out      map[string]any
		joined   bool
		sev1     bool
		openedAt time.Time
	}
	var rows []row
	truncated := false
	cursor := ""
	var items []client.IncidentSummary
	for page := 0; page < incidentPagesMax; page++ {
		p, err := org.ListIncidents(ctx, incidentPageSize, cursor)
		if err != nil {
			return failure(personRefusal(err, "the incident list"))
		}
		items = append(items, p.Items...)
		if p.NextCursor == nil || *p.NextCursor == "" {
			break
		}
		cursor = *p.NextCursor
		if page == incidentPagesMax-1 {
			truncated = true
		}
	}

	// Which incidents this checkout is in, and what the daemon knows of them.
	joinedKey := map[string]string{}
	if d.Rooms != nil {
		if rooms, err := d.Rooms(); err == nil {
			for _, r := range rooms {
				if readsWorkspace(r, d.WorkspaceKey) {
					joinedKey[r.IncidentID] = r.RoomKey
				}
			}
		}
	}
	status := map[string]daemon.RoomView{}
	if len(joinedKey) > 0 && d.Peek != nil {
		if rooms, err := d.Peek(); err == nil {
			for _, r := range rooms {
				status[r.IncidentID] = r
			}
		}
	}

	for _, it := range items {
		st := deref(it.Status)
		opened := parseTime(it.CreatedAt)
		if closedStatus(st) && (!all || opened.IsZero() || now.Sub(opened) > recentlyResolved) {
			continue
		}
		sev := strings.ToLower(strings.TrimSpace(deref(it.Severity)))
		key, joined := joinedKey[it.ID]
		out := map[string]any{
			"incidentId": it.ID,
			"displayId":  it.DisplayID,
			"title":      oneLineText(deref(it.Title), 160),
			"slug":       org.Slug,
			"severity":   sev,
			"status":     strings.ToLower(st),
			"openedAt":   it.CreatedAt,
			"joined":     joined,
			"roomKey":    key,
			"webUrl":     d.WebBase + "/o/" + org.Slug + "/incidents/" + it.ID,
		}
		if !opened.IsZero() {
			out["ageMs"] = now.Sub(opened).Milliseconds()
		}
		if it.Synthetic {
			out["practice"] = true
		}
		if joined {
			if v, ok := status[it.ID]; ok && v.Status != nil {
				here := []string{}
				for _, person := range v.Status.People {
					if person.Here {
						here = append(here, person.Name)
					}
				}
				out["here"] = here
				out["hereCount"] = len(here)
				if v.Status.Beacon != "" {
					out["beacon"] = v.Status.Beacon
				}
			}
		}
		rows = append(rows, row{out: out, joined: joined, sev1: sev == "sev1", openedAt: opened})
	}

	// The rooms this checkout is in first, unjoined SEV1s next, then newest.
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.joined != b.joined {
			return a.joined
		}
		if a.sev1 != b.sev1 {
			return a.sev1
		}
		return a.openedAt.After(b.openedAt)
	})
	incidents := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		incidents = append(incidents, r.out)
	}
	ans := map[string]any{"ok": true, "org": org.Slug, "incidents": incidents}
	if truncated {
		ans["truncated"] = true
	}
	return ans
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
