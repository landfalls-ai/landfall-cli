package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/landfalls-ai/landfall-cli/internal/daemon"
	"github.com/landfalls-ai/landfall-cli/internal/narrate"
)

func incidentRow(id, display, title, status, severity, createdAt string) map[string]any {
	return map[string]any{"id": id, "displayId": display, "title": title, "status": status, "severity": severity, "createdAt": createdAt, "number": 1}
}

func TestIncidentsListsOpenRoomsFirstWithoutPerIncidentReads(t *testing.T) {
	f := newFakeLandfall(t)
	page1 := map[string]any{
		"items": []any{
			incidentRow("inc-new-sev2", "Acme 12", "checkout slow", "open", "SEV2", "2026-10-08T15:50:00Z"),
			incidentRow("inc-sev1", "Acme 11", "orders-db-replica-lag", "investigating", "SEV1", "2026-10-08T15:00:00Z"),
			incidentRow("inc-resolved-recent", "Acme 10", "dns blip", "resolved", "sev3", "2026-10-08T10:00:00Z"),
		},
		"nextCursor": "c2",
	}
	page2 := map[string]any{
		"items": []any{
			incidentRow("inc-1", "Acme 7", "web-edge 5xx", "open", "sev2", "2026-10-08T12:00:00Z"),
			incidentRow("inc-resolved-old", "Acme 3", "old", "resolved", "sev1", "2026-10-01T10:00:00Z"),
		},
		"nextCursor": nil,
	}
	f.handle("GET /o/acme/incidents", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("authorization") != "Bearer "+personToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body := page1
		if r.URL.Query().Get("cursor") == "c2" {
			body = page2
		}
		if r.URL.Query().Get("limit") != "200" {
			t.Errorf("limit = %q, want the route's maximum", r.URL.Query().Get("limit"))
		}
		writeJSON200(w, body)
	})
	d := f.deps(true)
	d.Peek = func() ([]daemon.RoomView, error) {
		return []daemon.RoomView{{RoomKey: "rk1", IncidentID: "inc-1", Status: &narrate.RoomStatus{
			Beacon: "investigating",
			People: []narrate.Person{{Name: "dana", Here: true}, {Name: "carol", Here: false}},
		}}}, nil
	}

	ans := roundTrip(t, RunIncidents(context.Background(), IncidentsOptions{}, d))
	if ans["ok"] != true || ans["org"] != "acme" || ans["status"] != "open" {
		t.Fatalf("answer: %v", ans)
	}
	got := ans["incidents"].([]any)
	var order []string
	for _, x := range got {
		order = append(order, x.(map[string]any)["incidentId"].(string))
	}
	want := []string{"inc-1", "inc-sev1", "inc-new-sev2"}
	if len(order) != len(want) {
		t.Fatalf("incidents = %v, want %v (resolved ones left out)", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v: the room this checkout is in, then SEV1, then newest", order, want)
		}
	}
	joined := got[0].(map[string]any)
	if joined["joined"] != true || joined["roomKey"] != "rk1" || joined["hereCount"] != float64(1) || joined["beacon"] != "investigating" {
		t.Fatalf("joined row: %v", joined)
	}
	if here := joined["here"].([]any); len(here) != 1 || here[0] != "dana" {
		t.Fatalf("here = %v, want only who is here", here)
	}
	if joined["webUrl"] != "https://app.example/o/acme/incidents/inc-1" || joined["ageMs"] != float64(4*3600*1000) {
		t.Fatalf("webUrl/ageMs: %v", joined)
	}
	sev1 := got[1].(map[string]any)
	if sev1["severity"] != "sev1" || sev1["joined"] != false || sev1["roomKey"] != "" {
		t.Fatalf("unjoined row: %v", sev1)
	}
	for _, k := range []string{"here", "hereCount", "beacon"} {
		if _, has := sev1[k]; has {
			t.Fatalf("%s must only ride on a room this checkout is in: %v", k, sev1)
		}
	}
	// Two list reads, and nothing per incident.
	if calls := f.allCalls(); len(calls) != 2 {
		t.Fatalf("calls = %+v, want exactly the two list pages", calls)
	}
}

func TestIncidentsAllListsEveryIncident(t *testing.T) {
	f := newFakeLandfall(t)
	f.json("GET /o/acme/incidents", personToken, map[string]any{"items": []any{
		incidentRow("inc-open", "Acme 11", "checkout slow", "open", "sev2", "2026-10-08T15:00:00Z"),
		incidentRow("inc-resolved-recent", "Acme 10", "dns blip", "resolved", "sev3", "2026-10-08T10:00:00Z"),
		incidentRow("inc-resolved-old", "Acme 3", "old", "resolved", "sev1", "2026-10-01T10:00:00Z"),
	}})
	ans := roundTrip(t, RunIncidents(context.Background(), IncidentsOptions{Status: "all"}, f.deps(true)))
	got := incidentIDs(ans)
	sort.Strings(got)
	if strings.Join(got, ",") != "inc-open,inc-resolved-old,inc-resolved-recent" {
		t.Fatalf("all = %v, want every incident however old it is", got)
	}
	if ans["status"] != "all" {
		t.Fatalf("status = %v", ans["status"])
	}
}

func TestIncidentsSaysWhenTheListWasCut(t *testing.T) {
	f := newFakeLandfall(t)
	f.json("GET /o/acme/incidents", personToken, map[string]any{"items": []any{}, "nextCursor": "more"})
	ans := RunIncidents(context.Background(), IncidentsOptions{}, f.deps(true))
	if ans["truncated"] != true || len(f.allCalls()) != incidentPagesMax {
		t.Fatalf("answer %v after %d calls", ans, len(f.allCalls()))
	}
}

func TestIncidentsErrors(t *testing.T) {
	f := newFakeLandfall(t)
	wantFailure(t, RunIncidents(context.Background(), IncidentsOptions{}, f.deps(false)), "Sign in to list incidents: run landfall login.")

	f.status("GET /o/acme/incidents", http.StatusUnauthorized)
	wantFailure(t, RunIncidents(context.Background(), IncidentsOptions{}, f.deps(true)), msgLoginExpired)

	f.status("GET /o/acme/incidents", http.StatusForbidden)
	wantFailure(t, RunIncidents(context.Background(), IncidentsOptions{}, f.deps(true)), "Landfall refused to show you the incident list. Your sign-in may be for another organization.")

	d := f.deps(true)
	f.srv.Close()
	wantFailure(t, RunIncidents(context.Background(), IncidentsOptions{}, d), msgUnreachable)
}

func writeJSON200(w http.ResponseWriter, body any) {
	w.Header().Set("content-type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

// incidentIDs lists the incidentId of every row of an answer, in order.
func incidentIDs(ans map[string]any) []string {
	var ids []string
	for _, x := range ans["incidents"].([]any) {
		ids = append(ids, x.(map[string]any)["incidentId"].(string))
	}
	return ids
}

// viewRow is one incident of the table below. Its status may be absent.
type viewRow struct {
	id     string
	number int
	title  string
	status any // string, or nil for an incident that never had one
}

var viewRows = []viewRow{
	{"inc-open", 12, "checkout slow", "open"},
	{"inc-investigating", 11, "orders-db replica lag", "investigating"},
	{"inc-nostatus", 9, "no status yet", nil},
	{"inc-resolved", 10, "dns blip", "Resolved"},
	{"inc-mitigated", 8, "cdn 5xx", "mitigated"},
	{"inc-postmortem", 7, "cache stampede", "postmortem"},
	{"inc-archived", 3, "old checkout bug", "archived"},
}

func (r viewRow) json() map[string]any {
	return map[string]any{
		"id": r.id, "displayId": "Acme " + r.id, "number": r.number, "title": r.title,
		"status": r.status, "severity": "sev2", "createdAt": "2026-10-08T10:00:00Z",
	}
}

// serverView is what the Landfall API answers for status and q, written out
// from its contract rather than from the CLI's own filter: closed is one of
// resolved, closed, mitigated, recovered, done or archived (any case); open is
// every other status, or none; q is a case-insensitive title match, or the
// number when it is all digits.
func serverView(status, q string) []map[string]any {
	ended := map[string]bool{"resolved": true, "closed": true, "mitigated": true, "recovered": true, "done": true, "archived": true}
	var out []map[string]any
	for _, r := range viewRows {
		st, _ := r.status.(string)
		isEnded := r.status != nil && ended[strings.ToLower(st)]
		if (status == "open" && isEnded) || (status == "closed" && !isEnded) {
			continue
		}
		if q != "" {
			byTitle := strings.Contains(strings.ToLower(r.title), strings.ToLower(q))
			byNumber := false
			if n, err := json.Number(q).Int64(); err == nil && int(n) == r.number {
				byNumber = true
			}
			if !byTitle && !byNumber {
				continue
			}
		}
		out = append(out, r.json())
	}
	return out
}

// TestIncidentsAsksTheServerForTheView: the query string the route receives,
// and the rows printed, for each view and search, against a server that
// filters and against an older one that ignores both parameters and answers
// every incident. The two must print the same thing.
func TestIncidentsAsksTheServerForTheView(t *testing.T) {
	cases := []struct {
		name       string
		opts       IncidentsOptions
		wantStatus string
		wantQ      string
		want       []string
	}{
		{"default is open", IncidentsOptions{}, "open", "", []string{"inc-investigating", "inc-nostatus", "inc-open", "inc-postmortem"}},
		{"open", IncidentsOptions{Status: "open"}, "open", "", []string{"inc-investigating", "inc-nostatus", "inc-open", "inc-postmortem"}},
		{"closed", IncidentsOptions{Status: "closed"}, "closed", "", []string{"inc-archived", "inc-mitigated", "inc-resolved"}},
		{"all", IncidentsOptions{Status: "all"}, "all", "", []string{"inc-archived", "inc-investigating", "inc-mitigated", "inc-nostatus", "inc-open", "inc-postmortem", "inc-resolved"}},
		{"open and a title", IncidentsOptions{Q: "checkout"}, "open", "checkout", []string{"inc-open"}},
		{"all and a title", IncidentsOptions{Status: "all", Q: "checkout"}, "all", "checkout", []string{"inc-archived", "inc-open"}},
		{"a title in any case", IncidentsOptions{Status: "all", Q: "REPLICA"}, "all", "REPLICA", []string{"inc-investigating"}},
		{"a number", IncidentsOptions{Status: "all", Q: "10"}, "all", "10", []string{"inc-resolved"}},
		{"a number in the wrong view", IncidentsOptions{Status: "open", Q: "10"}, "open", "10", nil},
		{"a number is not a title", IncidentsOptions{Status: "all", Q: "1"}, "all", "1", nil},
	}
	for _, tc := range cases {
		for _, serverFilters := range []bool{true, false} {
			name := tc.name + "/older server"
			if serverFilters {
				name = tc.name + "/current server"
			}
			t.Run(name, func(t *testing.T) {
				f := newFakeLandfall(t)
				var queries []url.Values
				f.handle("GET /o/acme/incidents", func(w http.ResponseWriter, r *http.Request) {
					queries = append(queries, r.URL.Query())
					rows := []map[string]any{}
					if serverFilters {
						rows = serverView(r.URL.Query().Get("status"), r.URL.Query().Get("q"))
					} else {
						for _, vr := range viewRows {
							rows = append(rows, vr.json())
						}
					}
					writeJSON200(w, map[string]any{"items": rows, "nextCursor": nil})
				})
				ans := roundTrip(t, RunIncidents(context.Background(), tc.opts, f.deps(true)))
				if ans["ok"] != true {
					t.Fatalf("answer: %v", ans)
				}
				if len(queries) != 1 {
					t.Fatalf("%d list reads, want one", len(queries))
				}
				q := queries[0]
				if q.Get("status") != tc.wantStatus || q.Get("q") != tc.wantQ || q.Get("limit") != "200" || q.Has("cursor") {
					t.Fatalf("query = %v, want status=%q q=%q limit=200 and no cursor", q, tc.wantStatus, tc.wantQ)
				}
				if tc.wantQ == "" && q.Has("q") {
					t.Fatalf("an empty search must not be sent: %v", q)
				}
				got := incidentIDs(ans)
				sort.Strings(got)
				if strings.Join(got, ",") != strings.Join(tc.want, ",") {
					t.Fatalf("incidents = %v, want %v", got, tc.want)
				}
				if ans["status"] != tc.wantStatus {
					t.Fatalf("status = %v, want %q", ans["status"], tc.wantStatus)
				}
				if _, has := ans["q"]; has != (tc.wantQ != "") || (has && ans["q"] != tc.wantQ) {
					t.Fatalf("q = %v (present %v), want %q", ans["q"], has, tc.wantQ)
				}
			})
		}
	}
}

// TestIncidentsKeepsTheFilterOnEveryPage: the second page is asked for with
// the same view and search as the first, plus the cursor.
func TestIncidentsKeepsTheFilterOnEveryPage(t *testing.T) {
	f := newFakeLandfall(t)
	var queries []url.Values
	f.handle("GET /o/acme/incidents", func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query())
		if r.URL.Query().Get("cursor") == "c2" {
			writeJSON200(w, map[string]any{"items": []any{incidentRow("inc-2", "Acme 2", "disk full on db", "resolved", "sev3", "2026-10-02T10:00:00Z")}, "nextCursor": nil})
			return
		}
		writeJSON200(w, map[string]any{"items": []any{incidentRow("inc-5", "Acme 5", "disk full on web", "closed", "sev3", "2026-10-05T10:00:00Z")}, "nextCursor": "c2"})
	})
	ans := roundTrip(t, RunIncidents(context.Background(), IncidentsOptions{Status: "closed", Q: "disk full"}, f.deps(true)))
	if got := incidentIDs(ans); strings.Join(got, ",") != "inc-5,inc-2" {
		t.Fatalf("incidents = %v", got)
	}
	if len(queries) != 2 || queries[0].Has("cursor") || queries[1].Get("cursor") != "c2" {
		t.Fatalf("queries = %v, want the first page then cursor c2", queries)
	}
	for _, q := range queries {
		if q.Get("status") != "closed" || q.Get("q") != "disk full" {
			t.Fatalf("query = %v, want the view and search on every page", q)
		}
	}
}

// TestIncidentsOptions: the flags, chosen one way only, and the sentence
// when they cannot be used. Nothing here reaches the network.
func TestIncidentsOptions(t *testing.T) {
	long := strings.Repeat("é", incidentQueryMax)
	cases := []struct {
		name         string
		status       string
		closed, all  bool
		q            string
		want         IncidentsOptions
		wantSentence string
	}{
		{name: "no flags is open", want: IncidentsOptions{Status: "open"}},
		{name: "status open", status: "open", want: IncidentsOptions{Status: "open"}},
		{name: "status closed", status: "closed", want: IncidentsOptions{Status: "closed"}},
		{name: "status all in any case", status: " ALL ", want: IncidentsOptions{Status: "all"}},
		{name: "closed shorthand", closed: true, want: IncidentsOptions{Status: "closed"}},
		{name: "all shorthand", all: true, want: IncidentsOptions{Status: "all"}},
		{name: "search is trimmed", q: "  replica lag \n", want: IncidentsOptions{Status: "open", Q: "replica lag"}},
		{name: "a blank search is none", q: "   ", want: IncidentsOptions{Status: "open"}},
		{name: "the longest search", q: long, want: IncidentsOptions{Status: "open", Q: long}},
		{name: "a search one character too long", q: long + "x", wantSentence: "The search text is limited to 120 characters."},
		{name: "an unknown status", status: "pending", wantSentence: "--status takes open, closed or all."},
		{name: "closed and all", closed: true, all: true, wantSentence: "Choose one of --status, --closed and --all."},
		{name: "status and closed", status: "open", closed: true, wantSentence: "Choose one of --status, --closed and --all."},
		{name: "status and all", status: "closed", all: true, wantSentence: "Choose one of --status, --closed and --all."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, sentence := incidentsOptions(tc.status, tc.closed, tc.all, tc.q)
			if sentence != tc.wantSentence {
				t.Fatalf("sentence = %q, want %q", sentence, tc.wantSentence)
			}
			if sentence != "" {
				assertPlainSentence(t, sentence)
				return
			}
			if got != tc.want {
				t.Fatalf("options = %+v, want %+v", got, tc.want)
			}
		})
	}
}
