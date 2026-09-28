package cli

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/mcp"
)

// joinWaitForTool is how long a tool call waits for a join still in flight.
const joinWaitForTool = 20 * time.Second

// joinGate is serve's startup join in daemon mode, run off the MCP loop.
//
// A host gives an MCP server only seconds to answer initialize (Codex's
// default is about ten), and a join can take longer than that on a slow
// network. So initialize is answered at once and the join runs in the
// background; a tool call waits for it (bounded), and a join that failed is
// tried again on the next tool call, with the same config: its share link
// was already redeemed and recorded with the daemon, so the retry spends
// nothing. join_war_room does not wait for a failed join: the agent joining
// explicitly supersedes it.
type joinGate struct {
	join func(client.Config) error
	log  func(string)

	mu      sync.Mutex
	pending *client.Config // resolved, not yet joined; nil once joined
	running chan struct{}  // non-nil while an attempt runs; closed when it ends
	lastErr error
}

func newJoinGate(cfg client.Config, join func(client.Config) error, log func(string)) *joinGate {
	return &joinGate{join: join, log: log, pending: &cfg}
}

// start begins a join attempt unless one is running or the room is joined.
// Never blocks.
func (g *joinGate) start() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.startLocked()
}

func (g *joinGate) startLocked() chan struct{} {
	if g.pending == nil {
		return nil
	}
	if g.running != nil {
		return g.running
	}
	cfg := *g.pending
	done := make(chan struct{})
	g.running = done
	go func() {
		err := g.join(cfg)
		g.mu.Lock()
		if err == nil {
			g.pending = nil
		} else if g.log != nil {
			g.log(fmt.Sprintf("could not join incident %s yet (%v); the next tool call tries again.", cfg.IncidentID, err))
		}
		g.lastErr = err
		g.running = nil
		close(done)
		g.mu.Unlock()
	}()
	return done
}

// wait blocks until the room is joined, the attempt fails, or the budget
// runs out. A failed attempt with nothing running starts a new one first.
func (g *joinGate) wait(ctx context.Context, budget time.Duration) error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	if g.pending == nil {
		g.mu.Unlock()
		return nil
	}
	incident := g.pending.IncidentID
	done := g.startLocked()
	g.mu.Unlock()

	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		return fmt.Errorf("still joining incident %s; try again in a moment", incident)
	case <-ctx.Done():
		return ctx.Err()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.pending == nil {
		return nil
	}
	return fmt.Errorf("could not join incident %s: %v. Try again, or call join_war_room with a share link", incident, g.lastErr)
}

// abandon drops a join that has not succeeded: an explicit join_war_room
// replaces it. A running attempt is waited for (bounded) so it cannot land
// after the explicit join and overwrite it.
func (g *joinGate) abandon(ctx context.Context, budget time.Duration) {
	if g == nil {
		return
	}
	g.mu.Lock()
	running := g.running
	g.mu.Unlock()
	if running != nil {
		timer := time.NewTimer(budget)
		select {
		case <-running:
		case <-timer.C:
		case <-ctx.Done():
		}
		timer.Stop()
	}
	g.mu.Lock()
	g.pending = nil
	g.mu.Unlock()
}

// wrap makes every tool wait on the gate. The tool surface is unchanged.
func (g *joinGate) wrap(list []mcp.Tool) []mcp.Tool {
	if g == nil {
		return list
	}
	out := make([]mcp.Tool, len(list))
	for i, t := range list {
		t := t
		inner := t.Handler
		if inner != nil {
			if t.Name == "join_war_room" {
				t.Handler = func(ctx context.Context, args map[string]any) (string, error) {
					if err := g.wait(ctx, joinWaitForTool); err != nil {
						g.abandon(ctx, joinWaitForTool)
					}
					return inner(ctx, args)
				}
			} else {
				t.Handler = func(ctx context.Context, args map[string]any) (string, error) {
					if err := g.wait(ctx, joinWaitForTool); err != nil {
						return "", err
					}
					return inner(ctx, args)
				}
			}
		}
		out[i] = t
	}
	return out
}
