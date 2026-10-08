package cli

import (
	"context"
	"net/http"
	"testing"
)

const (
	incidentMemoryPath = "/o/acme/incidents/inc-1/memory"
	orgEntriesPath     = "/o/acme/memory/entries"
)

func TestBrainForTheRoomUsesTheIncidentScopedRead(t *testing.T) {
	f := newFakeLandfall(t)
	f.json("GET "+incidentMemoryPath, roomToken, map[string]any{"entries": []any{
		map[string]any{"id": "e1", "title": "cdn origin pool exhaustion", "summary": "Rolled back web-edge and raised the pool to 64.", "confidence": "established", "status": "active", "sourceIncidentIds": []any{"Acme 91", "Acme 95"}, "subjectType": "service", "subjectKey": "web-edge", "tags": []any{"cdn"}},
		map[string]any{"id": "e2", "title": "dns ttl", "confidence": "unconfirmed", "status": "conflicted", "sourceIncidentIds": []any{}},
	}})
	ans := roundTrip(t, RunBrain(context.Background(), "", "", f.deps(false)))
	matches := ans["matches"].([]any)
	if ans["ok"] != true || len(matches) != 2 {
		t.Fatalf("answer: %v", ans)
	}
	m := matches[0].(map[string]any)
	if m["title"] != "cdn origin pool exhaustion" || m["incident"] != "Acme 91" || m["confidence"] != "established" || m["summary"] != "Rolled back web-edge and raised the pool to 64." {
		t.Fatalf("match: %v", m)
	}
	for _, invented := range []string{"fix", "why", "status"} {
		if _, has := m[invented]; has {
			t.Fatalf("%s is not a field this entry carries: %v", invented, m)
		}
	}
	if second := matches[1].(map[string]any); second["status"] != "conflicted" {
		t.Fatalf("a conflicted entry says so: %v", second)
	}
}

func TestBrainSearchUsesThePersonAndMatchesHere(t *testing.T) {
	f := newFakeLandfall(t)
	f.json("GET "+orgEntriesPath, personToken, map[string]any{"canAdminister": false, "entries": []any{
		map[string]any{"id": "e1", "title": "dns ttl too long", "summary": "resolver cache", "confidence": "established", "status": "active", "kind": "lesson", "maturity": "settled", "sourceIncidents": []any{}},
		map[string]any{"id": "e2", "title": "cdn origin pool exhaustion", "summary": "web-edge 5xx on origin-b", "confidence": "established", "status": "active", "kind": "fact", "sourceIncidents": []any{map[string]any{"incidentId": "uuid-91", "incidentDisplayId": "Acme 91"}}},
		map[string]any{"id": "e3", "title": "unrelated", "summary": "disk full", "status": "active"},
	}})
	ans := roundTrip(t, RunBrain(context.Background(), "", "Origin pool 5xx", f.deps(true)))
	matches := ans["matches"].([]any)
	if len(matches) != 1 {
		t.Fatalf("matches: %v", matches)
	}
	m := matches[0].(map[string]any)
	if m["id"] != "e2" || m["incident"] != "Acme 91" || m["kind"] != "fact" {
		t.Fatalf("match: %v", m)
	}
	if calls := f.callsTo(orgEntriesPath); len(calls) != 1 || calls[0].Token != personToken {
		t.Fatalf("calls: %+v", calls)
	}
	if len(f.callsTo(incidentMemoryPath)) != 0 {
		t.Fatal("a search does not need the room's read")
	}
}

func TestBrainErrors(t *testing.T) {
	ctx := context.Background()
	wantFailure(t, RunBrain(ctx, "", "", noRoomDeps()), msgNoRoom)

	f := newFakeLandfall(t)
	wantFailure(t, RunBrain(ctx, "", "pool", f.deps(false)), "Sign in to search the company second brain: run landfall login.")

	f.status("GET "+incidentMemoryPath, http.StatusNotFound)
	wantFailure(t, RunBrain(ctx, "", "", f.deps(true)), msgBrainMissing)

	f.status("GET "+orgEntriesPath, http.StatusForbidden)
	wantFailure(t, RunBrain(ctx, "", "pool", f.deps(true)), msgBrainMissing)

	f.status("GET "+orgEntriesPath, http.StatusUnauthorized)
	wantFailure(t, RunBrain(ctx, "", "pool", f.deps(true)), msgLoginExpired)

	f.status("GET "+incidentMemoryPath, http.StatusUnauthorized)
	wantFailure(t, RunBrain(ctx, "", "", f.deps(true)), msgRoomExpired)
}
