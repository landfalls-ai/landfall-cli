package cli

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/mcp"
)

func gatedTools(g *joinGate, ran *[]string, mu *sync.Mutex) []mcp.Tool {
	tool := func(name string) mcp.Tool {
		return mcp.Tool{Name: name, Handler: func(context.Context, map[string]any) (string, error) {
			mu.Lock()
			*ran = append(*ran, name)
			mu.Unlock()
			return "ok", nil
		}}
	}
	return g.wrap([]mcp.Tool{tool("get_brief"), tool("join_war_room")})
}

// A join that failed is tried again on the next tool call, with the same
// config: the link it came from was already redeemed and recorded.
func TestAFailedStartupJoinIsRetriedOnTheNextToolCall(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	g := newJoinGate(client.Config{IncidentID: "inc-1", Token: "t"}, func(cfg client.Config) error {
		mu.Lock()
		defer mu.Unlock()
		attempts++
		if cfg.Token != "t" {
			t.Errorf("the retry must use the same config, got %+v", cfg)
		}
		if attempts == 1 {
			return errors.New("HTTP 503")
		}
		return nil
	}, nil)
	var ran []string
	tools := gatedTools(g, &ran, &mu)
	g.start()

	_, err := tools[0].Handler(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "could not join incident inc-1") {
		// The first call may land on the first attempt's failure, or on a
		// retry that already succeeded; both are acceptable, a hang is not.
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if _, err := tools[0].Handler(context.Background(), nil); err != nil {
		t.Fatalf("the next tool call retries the join and runs: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestAToolCallWaitsForAJoinStillInFlightButNotForever(t *testing.T) {
	release := make(chan struct{})
	g := newJoinGate(client.Config{IncidentID: "inc-1"}, func(client.Config) error { <-release; return nil }, nil)
	g.start()
	if err := g.wait(context.Background(), 50*time.Millisecond); err == nil || !strings.Contains(err.Error(), "still joining incident inc-1") {
		t.Fatalf("a join past the budget reads as still joining: %v", err)
	}
	close(release)
	if err := g.wait(context.Background(), time.Second); err != nil {
		t.Fatalf("once the join lands the tool runs: %v", err)
	}
}

func TestAnExplicitJoinWarRoomSupersedesAFailedStartupJoin(t *testing.T) {
	var mu sync.Mutex
	g := newJoinGate(client.Config{IncidentID: "inc-1"}, func(client.Config) error { return errors.New("HTTP 410") }, nil)
	var ran []string
	tools := gatedTools(g, &ran, &mu)
	g.start()
	if _, err := tools[1].Handler(context.Background(), nil); err != nil {
		t.Fatalf("join_war_room must run even when the startup join failed: %v", err)
	}
	if _, err := tools[0].Handler(context.Background(), nil); err != nil {
		t.Fatalf("after an explicit join the startup join is no longer waited on: %v", err)
	}
}

func TestANilGateChangesNothing(t *testing.T) {
	var g *joinGate
	list := []mcp.Tool{{Name: "x"}}
	if got := g.wrap(list); len(got) != 1 || got[0].Name != "x" {
		t.Fatal("no gate, same tools")
	}
	g.start()
	if err := g.wait(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
}
