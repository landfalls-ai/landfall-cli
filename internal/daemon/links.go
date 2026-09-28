package daemon

import (
	"context"
	"sync"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

// A share link is single-use. The daemon keeps a book of the links (by
// LinkHash, never the link, which carries a credential) that this machine has
// redeemed or is redeeming, so that:
//
//   - a second harness handed the same link joins with the session the first
//     redeem gave, instead of redeeming it again (which the server refuses);
//   - a link whose redeem succeeded but whose join then failed is not burned:
//     the session is recorded the moment the redeem lands, before any join;
//   - two harnesses started together with the same --link do not both
//     redeem: the first to ask holds the claim, the second waits for it.

// linkClaimTTL is how long a claim holds before another caller may take it
// over: a claimer that crashed mid-redeem must not block the link for good.
const linkClaimTTL = 30 * time.Second

type linkEntry struct {
	cfg     *client.Config // what the redeem gave; nil until it has
	claimed time.Time      // when the current claim was taken; zero when none
	done    chan struct{}  // closed when the current claim settles
}

type linkBook struct {
	mu sync.Mutex
	m  map[string]*linkEntry
}

func (b *linkBook) fulfil(hash string, cfg client.Config) {
	if hash == "" {
		return
	}
	cfg.AgentLabel = ""
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.m == nil {
		b.m = map[string]*linkEntry{}
	}
	e, ok := b.m[hash]
	if !ok {
		e = &linkEntry{}
		b.m[hash] = e
	}
	e.cfg = &cfg
	e.settleLocked()
}

func (b *linkBook) release(hash string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e, ok := b.m[hash]
	if !ok {
		return
	}
	e.settleLocked()
	if e.cfg == nil {
		delete(b.m, hash)
	}
}

func (e *linkEntry) settleLocked() {
	e.claimed = time.Time{}
	if e.done != nil {
		close(e.done)
		e.done = nil
	}
}

// lookupOrClaimLink answers `link`: the config a share link already gave this
// machine (from a room it opened, or a redeem recorded before any join), or
// the claim to redeem it, waiting up to LinkWait for a sibling that holds the
// claim right now.
func (d *Daemon) lookupOrClaimLink(ctx context.Context, hash string) Response {
	if hash == "" {
		return fail("link needs a link")
	}
	deadline := time.Now().Add(LinkWait)
	for {
		for _, room := range d.rooms() {
			if room.HasLink(hash) {
				cfg := room.SessionConfig()
				res := ok()
				res.RoomKey, res.Room = room.Key, &cfg
				return res
			}
		}
		b := &d.links
		b.mu.Lock()
		if b.m == nil {
			b.m = map[string]*linkEntry{}
		}
		e := b.m[hash]
		if e != nil && e.cfg != nil {
			cfg := *e.cfg
			b.mu.Unlock()
			res := ok()
			res.Room = &cfg
			return res
		}
		if e == nil || e.claimed.IsZero() || time.Since(e.claimed) > linkClaimTTL {
			if e == nil {
				e = &linkEntry{}
				b.m[hash] = e
			} else if e.done != nil {
				close(e.done) // a stale claim: wake anyone still waiting on it
			}
			e.claimed, e.done = time.Now(), make(chan struct{})
			b.mu.Unlock()
			res := ok()
			res.Claimed = true
			return res
		}
		done := e.done
		b.mu.Unlock()
		wait := time.Until(deadline)
		if wait <= 0 {
			return fail("another session on this machine is still redeeming this link; try again in a moment")
		}
		timer := time.NewTimer(wait)
		select {
		case <-done:
			timer.Stop()
		case <-timer.C:
			return fail("another session on this machine is still redeeming this link; try again in a moment")
		case <-ctx.Done():
			timer.Stop()
			return fail("the daemon is stopping")
		}
	}
}
