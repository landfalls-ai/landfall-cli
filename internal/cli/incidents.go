package cli

// incidents.go: `landfall incidents [--status open|closed|all] [--q <text>]`:
// the organization's incidents for the Claude Code mod's switchboard
// (contract §3, review #6). Open ones by default.
//
// One list read with the person's own token (an edge token is refused on the
// org-level route), keyset-paged, at most incidentPagesMax pages. The server
// filters: the route takes status=open|closed|all and q=<text>, so the pages
// hold only what was asked for and the two-page cap no longer lets a long
// history of closed incidents push an open one out of view. A server that
// predates those parameters ignores them and answers every incident, so the
// same filters run here once more; against a current server that second pass
// removes nothing. Presence and Beacon's state are NOT read per incident:
// that is the N+1 burst that trips the deployed per-IP rate limit. They come
// from the room daemon, and only for rooms this checkout is in, where it
// already holds them.

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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
	// incidentQueryMax is the longest search text the route accepts.
	incidentQueryMax = 120
)

// The three views of the list, as the route spells them.
const (
	incidentsOpen   = "open"
	incidentsClosed = "closed"
	incidentsAll    = "all"
)

// endedIncidentStatuses mirrors ENDED_INCIDENT_STATUSES in the Landfall
// API's contracts: an incident whose status is one of these (compared
// case-insensitively, as the route does) is closed; any other status, or none,
// is open. Keep it equal to that list, because the filter below is only a
// fallback for a server that does not filter and must agree with one that does.
var endedIncidentStatuses = map[string]bool{
	"resolved":  true,
	"closed":    true,
	"mitigated": true,
	"recovered": true,
	"done":      true,
	"archived":  true,
}

// IncidentsOptions says which incidents to list.
type IncidentsOptions struct {
	// Status is "open" (the default when empty), "closed" or "all".
	Status string
	// Q is search text, matched against the title, or against the number when
	// it is all digits. Empty means no search.
	Q string
}

func newIncidentsCommand(ui *UI) *cobra.Command {
	var status, q string
	var closed, all bool
	c := newReadCommand(ui, "incidents", func(cmd *cobra.Command, ws hooks.Workspace, _ string) map[string]any {
		opts, problem := incidentsOptions(status, closed, all, q)
		if problem != "" {
			// A read command always answers one JSON line, so a bad flag is
			// an answer, not a usage error.
			return failure(problem)
		}
		return RunIncidents(cmdContext(cmd), opts, defaultReadDeps(ws))
	})
	c.Flags().StringVar(&status, "status", "", "which incidents: open (the default), closed or all")
	c.Flags().BoolVar(&closed, "closed", false, "closed incidents only (same as --status closed)")
	c.Flags().BoolVar(&all, "all", false, "every incident, open and closed (same as --status all)")
	c.Flags().StringVar(&q, "q", "", "only incidents whose title contains this text, or whose number it is")
	return c
}

// incidentsOptions reads the flags. --status, --closed and --all choose the
// same thing three ways, so at most one may be given. The second result is a
// sentence for the person when the flags cannot be used.
func incidentsOptions(status string, closed, all bool, q string) (IncidentsOptions, string) {
	chosen := 0
	if strings.TrimSpace(status) != "" {
		chosen++
	}
	if closed {
		chosen++
	}
	if all {
		chosen++
	}
	if chosen > 1 {
		return IncidentsOptions{}, "Choose one of --status, --closed and --all."
	}
	opts := IncidentsOptions{Status: incidentsOpen}
	switch v := strings.ToLower(strings.TrimSpace(status)); {
	case closed:
		opts.Status = incidentsClosed
	case all:
		opts.Status = incidentsAll
	case v == "":
	case v == incidentsOpen || v == incidentsClosed || v == incidentsAll:
		opts.Status = v
	default:
		return IncidentsOptions{}, "--status takes open, closed or all."
	}
	opts.Q = strings.TrimSpace(q)
	if utf8.RuneCountInString(opts.Q) > incidentQueryMax {
		return IncidentsOptions{}, fmt.Sprintf("The search text is limited to %d characters.", incidentQueryMax)
	}
	return opts, ""
}

// listedIncident is the filter the route applies, applied again here: the
// view (open, closed or all) by status, and the search by title or number.
func listedIncident(it client.IncidentSummary, opts IncidentsOptions) bool {
	ended := it.Status != nil && endedIncidentStatuses[strings.ToLower(*it.Status)]
	switch opts.Status {
	case incidentsClosed:
		if !ended {
			return false
		}
	case incidentsAll:
	default:
		if ended {
			return false
		}
	}
	if opts.Q == "" {
		return true
	}
	if strings.Contains(strings.ToLower(deref(it.Title)), strings.ToLower(opts.Q)) {
		return true
	}
	if !allDigits(opts.Q) {
		return false
	}
	n, err := strconv.ParseInt(opts.Q, 10, 64)
	return err == nil && n == it.Number
}

// allDigits reports a non-empty string of ASCII digits.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
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
func RunIncidents(ctx context.Context, opts IncidentsOptions, d ReadDeps) map[string]any {
	if opts.Status == "" {
		opts.Status = incidentsOpen
	}
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
		p, err := org.ListIncidents(ctx, client.IncidentListQuery{Limit: incidentPageSize, Cursor: cursor, Status: opts.Status, Q: opts.Q})
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
		if !listedIncident(it, opts) {
			continue
		}
		st := deref(it.Status)
		opened := parseTime(it.CreatedAt)
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
	ans := map[string]any{"ok": true, "org": org.Slug, "status": opts.Status, "incidents": incidents}
	if opts.Q != "" {
		ans["q"] = opts.Q
	}
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
