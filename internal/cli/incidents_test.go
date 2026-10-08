package cli

import (
	"context"
	"encoding/json"
	"net/http"
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

	ans := roundTrip(t, RunIncidents(context.Background(), false, d))
	if ans["ok"] != true || ans["org"] != "acme" {
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

func TestIncidentsAllAddsRecentlyResolved(t *testing.T) {
	f := newFakeLandfall(t)
	f.json("GET /o/acme/incidents", personToken, map[string]any{"items": []any{
		incidentRow("inc-resolved-recent", "Acme 10", "dns blip", "resolved", "sev3", "2026-10-08T10:00:00Z"),
		incidentRow("inc-resolved-old", "Acme 3", "old", "resolved", "sev1", "2026-10-01T10:00:00Z"),
	}})
	ans := roundTrip(t, RunIncidents(context.Background(), true, f.deps(true)))
	got := ans["incidents"].([]any)
	if len(got) != 1 || got[0].(map[string]any)["incidentId"] != "inc-resolved-recent" {
		t.Fatalf("--all = %v, want only the one resolved in the last day", got)
	}
}

func TestIncidentsSaysWhenTheListWasCut(t *testing.T) {
	f := newFakeLandfall(t)
	f.json("GET /o/acme/incidents", personToken, map[string]any{"items": []any{}, "nextCursor": "more"})
	ans := RunIncidents(context.Background(), false, f.deps(true))
	if ans["truncated"] != true || len(f.allCalls()) != incidentPagesMax {
		t.Fatalf("answer %v after %d calls", ans, len(f.allCalls()))
	}
}

func TestIncidentsErrors(t *testing.T) {
	f := newFakeLandfall(t)
	wantFailure(t, RunIncidents(context.Background(), false, f.deps(false)), "Sign in to list incidents: run landfall login.")

	f.status("GET /o/acme/incidents", http.StatusUnauthorized)
	wantFailure(t, RunIncidents(context.Background(), false, f.deps(true)), msgLoginExpired)

	f.status("GET /o/acme/incidents", http.StatusForbidden)
	wantFailure(t, RunIncidents(context.Background(), false, f.deps(true)), "Landfall refused to show you the incident list. Your sign-in may be for another organization.")

	d := f.deps(true)
	f.srv.Close()
	wantFailure(t, RunIncidents(context.Background(), false, d), msgUnreachable)
}

func writeJSON200(w http.ResponseWriter, body any) {
	w.Header().Set("content-type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}
