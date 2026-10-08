package cli

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/narrate"
)

// TestWatchPeopleCarryHumanActorID: each `status.people[]` row the watch
// stream writes names the person's humanActorId, so the console selects a
// person by id (console spec §9), never by a name two people can share.
func TestWatchPeopleCarryHumanActorID(t *testing.T) {
	on := true
	frame := &client.ContextFrame{Participants: []client.Participant{
		{DisplayName: "bob", Kind: "member", HumanActorID: "u-bob", AgentInstanceID: "web:u-bob:t", Active: &on},
		{DisplayName: "bob", Kind: "member", HumanActorID: "u-bob", AgentInstanceID: "i-1", EdgeAgentLabel: "bob-claude-code", Active: &on},
		{DisplayName: "bob", Kind: "member", HumanActorID: "u-bob-2", AgentInstanceID: "web:u-bob-2:t", Active: &on},
	}}
	st := narrate.StatusOf(frame, "u-bob", "")
	out := &lockedBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = Watch(ctx, WatchDeps{Snapshot: func() WatchSnapshot {
			return WatchSnapshot{Type: "rooms", Rooms: []WatchRoom{{RoomKey: "r1", Status: &st}}}
		}, Out: out, Tick: time.Hour})
	}()
	waitFor(t, func() bool { return outLines(out)[0] != "" }, "the watch loop")
	cancel()

	var line struct {
		Rooms []struct {
			Status struct {
				People []map[string]any `json:"people"`
			} `json:"status"`
		} `json:"rooms"`
	}
	if err := json.Unmarshal([]byte(outLines(out)[0]), &line); err != nil {
		t.Fatalf("not JSON: %s", outLines(out)[0])
	}
	people := line.Rooms[0].Status.People
	if len(people) != 2 {
		t.Fatalf("people = %v", people)
	}
	if people[0]["humanActorId"] != "u-bob" || people[0]["you"] != true || people[1]["humanActorId"] != "u-bob-2" {
		t.Fatalf("people = %v, want each row keyed by humanActorId", people)
	}
}
