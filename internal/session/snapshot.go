package session

import (
	"context"
	"sync"
	"time"
)

// snapshot is the coalesced background read behind `attention` and
// `divergence` — the hardest concurrency behavior in this port.
//
// The Node original (`src/tools.mjs`'s refreshAttention/settleAttention) uses a
// PROMISE-IDENTITY guard: every write is gated on
// `session.attentionInFlight === inFlight`, and invalidation is done by
// CLEARING that pointer rather than by starting a new read. Go has no promise
// identity to key on across a room switch, so this uses a `generation` counter
// PLUS a shared future (a `done` channel). Four properties, each verified
// against the Node source and its 61-case attention.test.mjs:
//
//  1. ONLY THE CURRENT IN-FLIGHT READ MAY WRITE THE SNAPSHOT. A superseded read
//     discards its own answer and touches nothing — including on failure, where
//     its re-dirty is also suppressed: its failure says nothing about the
//     snapshot the current read owns.
//  2. COALESCING. A second caller arriving while a read is in flight JOINS that
//     read rather than issuing another, so a burst of claim events costs one
//     request, not one per event.
//  3. `dirty` IS CLEARED ON ENTRY, NOT ON SUCCESS. An event arriving DURING a
//     read re-dirties the snapshot, so the next caller reads again rather than
//     trusting an answer computed before that event landed.
//  4. A READ THAT MISSES ITS BUDGET IS ABANDONED. `settle` clears the in-flight
//     marker AND re-dirties AT ABANDON TIME (so the next caller starts fresh
//     instead of joining a wedged read); the abandoned read is still free to
//     land later, at which point property 1 makes it discard its answer and
//     skip its own re-dirty.
//
// The generation counter bumps on EVERY invalidation — the timeout abandon
// above and the room-switch reset in Session.JoinWarRoom — not merely on read
// starts. Bump-on-read-start alone would leave the old room's still-in-flight
// read satisfying `gen == current`, so it would write incident A's claims into
// incident B's snapshot; `claimSeq` is a per-incident counter, so acting on the
// resulting "vote requested: claim #N" would record a position on an unrelated
// claim.
type snapshot[T any] struct {
	mu       sync.Mutex
	value    *T // nil until a read has actually landed
	dirty    bool
	gen      uint64
	inFlight *read

	// wg tracks spawned reads so a test (or a shutdown) can wait for the
	// fire-and-forget ones rather than racing them.
	wg sync.WaitGroup
}

// read is one in-flight fetch. `done` is the shared future every joining
// caller waits on.
type read struct {
	gen  uint64
	done chan struct{}
}

type fetchFunc[T any] func(ctx context.Context) (*T, error)

// startPolicy decides whether a caller starts a read or only joins one that is
// already running.
type startPolicy int

const (
	// always starts a read whenever none is in flight (refreshAttention).
	always startPolicy = iota
	// ifDirty starts one only when the snapshot is dirty (settleAttention's
	// `if (!inFlight && dirty) refresh()`).
	ifDirty
	// ifDirtyOrEmpty starts one when dirty OR nothing has ever landed
	// (flushVoteRequests' `if (dirty || attention === null) await refresh()`).
	ifDirtyOrEmpty
)

// begin starts a read (or joins the in-flight one) per `policy`, returning the
// read to wait on — nil when the policy declined to start one and none was
// running.
func (s *snapshot[T]) begin(ctx context.Context, fetch fetchFunc[T], policy startPolicy) *read {
	s.mu.Lock()
	// The two conditional policies differ in WHERE the condition sits relative
	// to the join, and the difference is load-bearing:
	//
	//   settle          → `if (!inFlight && dirty) refresh()`, then waits on
	//                     whatever is in flight — so it joins a running read
	//                     even when the snapshot is clean.
	//   flushVoteRequests → `if (dirty || attention === null) await refresh()`
	//                     — a clean, already-landed snapshot does not call
	//                     refresh at all, so it must NOT join a read either;
	//                     blocking the fast path on a background read is
	//                     exactly the latency this channel is designed to avoid.
	if policy == ifDirtyOrEmpty && !s.dirty && s.value != nil {
		s.mu.Unlock()
		return nil
	}
	if s.inFlight != nil {
		// Property 2: join the read already running rather than issuing another.
		r := s.inFlight
		s.mu.Unlock()
		return r
	}
	if policy == ifDirty && !s.dirty {
		s.mu.Unlock()
		return nil
	}
	// Property 3: cleared on ENTRY, so an invalidation arriving during the read
	// leaves the snapshot dirty for the next caller.
	s.dirty = false
	r := &read{gen: s.gen, done: make(chan struct{})}
	s.inFlight = r
	s.wg.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.wg.Done()
		next, err := fetch(ctx)
		s.mu.Lock()
		// Property 1: only the current read may write. A superseded read
		// (abandoned by settle, or discarded by a room switch) fails this test
		// and touches nothing at all — neither the value nor the dirty flag.
		if s.gen == r.gen && s.inFlight == r {
			if err != nil {
				// Leave the previous answer in place rather than blanking it —
				// a stale vote request is recoverable, a silently dropped one
				// is the bug. Re-dirty so the next caller retries.
				s.dirty = true
			} else {
				s.value = next
			}
			// Cleared before the future is signalled, so the next caller starts
			// a fresh read rather than joining a finished one.
			s.inFlight = nil
		}
		s.mu.Unlock()
		close(r.done)
	}()
	return r
}

// refresh starts (or joins) a read and waits for it. Never returns an error:
// an unreachable server means "nothing known to be waiting", exactly as it
// does for the live socket.
func (s *snapshot[T]) refresh(ctx context.Context, fetch fetchFunc[T]) *T {
	if r := s.begin(ctx, fetch, always); r != nil {
		select {
		case <-r.done:
		case <-ctx.Done():
		}
	}
	return s.get()
}

// refreshAsync starts (or joins) a read without waiting — the fire-and-forget
// trigger fired by an arriving room event and by a `query`-kind contribution.
func (s *snapshot[T]) refreshAsync(ctx context.Context, fetch fetchFunc[T]) {
	s.begin(ctx, fetch, always)
}

// refreshIfStale is `flushVoteRequests`' forced read: it fetches when the
// snapshot is dirty or has never landed, and does nothing otherwise — so a
// quiet room costs ZERO extra requests per tool call.
func (s *snapshot[T]) refreshIfStale(ctx context.Context, fetch fetchFunc[T]) *T {
	if r := s.begin(ctx, fetch, ifDirtyOrEmpty); r != nil {
		select {
		case <-r.done:
		case <-ctx.Done():
		}
	}
	return s.get()
}

// settle makes the snapshot as current as it can be made within `timeout`,
// then returns whatever there is. A hook that hangs is a hook that gets
// uninstalled, so the wait is bounded and a slow server costs a
// possibly-stale answer, never a stuck agent.
func (s *snapshot[T]) settle(ctx context.Context, fetch fetchFunc[T], timeout time.Duration) *T {
	r := s.begin(ctx, fetch, ifDirty)
	if r == nil {
		return s.get()
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-r.done:
	case <-ctx.Done():
	case <-timer.C:
		s.mu.Lock()
		if s.inFlight == r {
			// Stop treating a read this slow as the one everybody joins: a
			// request that never returns would otherwise wedge the coalescing
			// for the rest of the session and silently disable every later
			// refresh. Re-dirtied HERE, at abandon time — the read is still
			// free to land later, where property 1 makes it write nothing.
			s.inFlight = nil
			s.dirty = true
			s.gen++
		}
		s.mu.Unlock()
	}
	return s.get()
}

// invalidate is the room-switch reset: forget the answer, forget that anything
// is in flight, and bump the generation so the read still running against the
// OLD room can never write into the new room's snapshot.
func (s *snapshot[T]) invalidate() {
	s.mu.Lock()
	s.value = nil
	s.dirty = true
	s.inFlight = nil
	s.gen++
	s.mu.Unlock()
}

// markDirty flags the snapshot for a re-read without discarding what it holds.
func (s *snapshot[T]) markDirty() {
	s.mu.Lock()
	s.dirty = true
	s.mu.Unlock()
}

// get returns the last landed answer, or nil if none ever landed.
func (s *snapshot[T]) get() *T {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value
}

// set overwrites the snapshot directly. Only for tests and for a caller
// restoring known state — the read path never uses it.
func (s *snapshot[T]) set(v *T) {
	s.mu.Lock()
	s.value = v
	s.mu.Unlock()
}

// isDirty reports whether the next caller should re-read.
func (s *snapshot[T]) isDirty() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dirty
}

// setDirty forces the flag, for tests reconstructing a mid-session state.
func (s *snapshot[T]) setDirty(v bool) {
	s.mu.Lock()
	s.dirty = v
	s.mu.Unlock()
}

// inFlightRead exposes the current read for tests asserting the coalescing
// itself ("precondition: a read is genuinely in flight").
func (s *snapshot[T]) inFlightRead() *read {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inFlight
}

// wait blocks until every read this snapshot spawned has landed. Tests use it
// to make a fire-and-forget refresh deterministic.
func (s *snapshot[T]) wait() { s.wg.Wait() }
