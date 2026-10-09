package cli

import (
	"context"
	"net/http"
	"testing"
)

const commsPath = "/o/acme/incidents/inc-1/comms/messages"

func TestCommsListsUpdatesNewestFirstReadOnly(t *testing.T) {
	f := newFakeLandfall(t)
	f.json("GET "+commsPath, roomToken, map[string]any{"items": []any{
		map[string]any{"messageId": "m1", "version": "v1", "channel": "slack", "body": "We are investigating elevated errors.", "kind": "status-update", "status": "sent", "approvedByHuman": true, "approvedBy": "user-carol", "approvedByName": "carol", "sent": true, "simulated": false},
		map[string]any{"messageId": "m2", "version": "v2", "channel": "email:cto@acme.test", "body": "Mitigation in progress.", "kind": "milestone", "status": "drafted", "approvedByHuman": false, "sent": false},
	}})
	ans := roundTrip(t, RunComms(context.Background(), "", f.deps(false)))
	msgs := ans["messages"].([]any)
	if ans["ok"] != true || len(msgs) != 2 {
		t.Fatalf("answer: %v", ans)
	}
	newest, sent := msgs[0].(map[string]any), msgs[1].(map[string]any)
	if newest["id"] != "m2" || newest["state"] != "draft" || newest["channel"] != "email:cto@acme.test" {
		t.Fatalf("draft: %v", newest)
	}
	if _, has := newest["approvedBy"]; has {
		t.Fatalf("an unapproved draft names no approver: %v", newest)
	}
	if sent["state"] != "sent" || sent["approvedBy"] != "carol" || sent["text"] != "We are investigating elevated errors." {
		t.Fatalf("sent: %v", sent)
	}
	// Only the read: nothing approved or sent from here.
	for _, c := range f.allCalls() {
		if c.Method != http.MethodGet {
			t.Fatalf("comms must only read: %+v", c)
		}
	}
}

func TestCommsErrors(t *testing.T) {
	ctx := context.Background()
	wantFailure(t, RunComms(ctx, "", noRoomDeps()), msgNoRoom)

	f := newFakeLandfall(t)
	f.status("GET "+commsPath, http.StatusForbidden)
	wantFailure(t, RunComms(ctx, "", f.deps(true)), "Landfall refused to show the stakeholder updates to this room's session.")

	f.status("GET "+commsPath, http.StatusUnauthorized)
	wantFailure(t, RunComms(ctx, "", f.deps(true)), msgRoomExpired)

	f.handle("GET "+commsPath, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`[1,2]`)) })
	wantFailure(t, RunComms(ctx, "", f.deps(true)), "Landfall sent stakeholder updates this CLI could not read.")
}
