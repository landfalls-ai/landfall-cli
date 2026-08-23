// doorbell.go — the file a `FileChanged` hook watches (#227, story #190). A Go
// port of `src/hooks/doorbell.mjs`.
//
// The ticket originally called this a SPOOL: the bridge would write incoming
// room events to a per-incident file and the hook would read them out of it.
// The #225 socket decision changed that, and improved it — the file stops being
// the transport and becomes a doorbell.
//
//	serve enqueues an event ─► pending goes 0 → non-empty ─► append a marker
//	                                                                 │
//	Claude Code's file watcher fires ◄─────────────────────────────────┘
//	                 │
//	                 └─► landfall hooks file-changed ──socket──► peek/consume
//
// Two problems dissolve in that change:
//
//   - "Rotate/truncate the spool after consumption" with more than one local
//     session. There is nothing to rotate. Two agents on one incident have two
//     serve processes, two sockets and two cursors, so whoever wakes first no
//     longer consumes the other's context. The marker is idempotent and can be
//     truncated by anyone at any time without losing anything, because it never
//     held the events.
//
//   - Room content in the user's repository. A marker line carries a timestamp,
//     a pid and a count — never an event, never a finding, never a name. The
//     incident stays on the socket, which is readable by one user account, and
//     out of a directory that gets grepped, backed up and occasionally committed.
//
// The 0 → non-empty EDGE is what rings, not every enqueue: a busy room would
// otherwise rewrite the file on every event and wake the hook on each one, for
// context the session is already about to be shown.
package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DoorbellDir is the directory name reserved in the workspace. Self-ignoring —
// see ensureDoorbellDir.
const DoorbellDir = ".landfall"

// DoorbellFile is the marker's filename, and it is NOT free choice — Claude
// Code's `FileChanged` matcher is a list of literal filenames whose exact-match
// set is "letters, digits, `_`, and `|`". A hyphen drops the whole matcher onto
// the regular-expression path instead, so `room-events` would only work by way
// of a regex that happens to match. `room_events` stays on the documented
// exact-match path, which is why the underscore is load-bearing rather than
// stylistic. See spec.go.
const DoorbellFile = "room_events"

// maxMarkerBytes — past this, the marker file is rewritten rather than appended
// to.
const maxMarkerBytes = 8 * 1024

// DoorbellPath is the marker file for one workspace.
func DoorbellPath(cwd string) string {
	return filepath.Join(cwd, DoorbellDir, DoorbellFile)
}

// Doorbell is a doorbell bound to one workspace. Ring is best-effort and never
// fails outward: a read-only checkout must not take down a serve process, it
// just means an idle session finds out at its next tool call instead.
type Doorbell struct {
	// Path is the marker file this doorbell writes.
	Path string

	pid int
	now func() time.Time
	log func(string)

	mu     sync.Mutex
	warned bool
}

// DoorbellOptions configures NewDoorbell. The zero value is the production one.
type DoorbellOptions struct {
	// Cwd is the workspace root. Empty means os.Getwd().
	Cwd string
	// PID is stamped on each marker line. Zero means os.Getpid().
	PID int
	// Now supplies the timestamp. Nil means time.Now.
	Now func() time.Time
	// Log receives the single warning a broken doorbell produces.
	Log func(string)
}

// NewDoorbell builds a doorbell for one workspace.
func NewDoorbell(opts DoorbellOptions) *Doorbell {
	cwd := opts.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	pid := opts.PID
	if pid == 0 {
		pid = os.Getpid()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Doorbell{Path: DoorbellPath(cwd), pid: pid, now: now, log: opts.Log}
}

// Ring writes one marker line. Best-effort and never returns an error: the
// caller is a realtime event handler, and a failure here costs a late nudge,
// not a lost room event (the events are on the socket, not here).
//
// The CALLER owns the 0 → non-empty edge — see RingOnEdge, and `serve`'s
// realtime handler, which is where `bin/landfall.mjs:167-173` puts it.
func (d *Doorbell) Ring(pending int) {
	if err := d.ring(pending); err != nil {
		d.mu.Lock()
		first := !d.warned
		d.warned = true
		d.mu.Unlock()
		if first && d.log != nil {
			// Once per process — a nudge, not a per-event complaint.
			d.log(fmt.Sprintf("doorbell unavailable (%v) — an idle session will see room context at its next tool call.", err))
		}
	}
}

// RingOnEdge is Ring guarded by the rule that makes the doorbell a doorbell: it
// rings ONLY on the 0 → non-empty pending-count edge.
//
// A session that already owes context will be shown a newly arrived event too
// when the bell it has yet to answer is answered; re-ringing on every event
// would wake an idle agent once per message in a busy room. The guard lives
// here, next to the file it protects, so `serve` cannot get it subtly wrong.
//
//	wasEmpty — len(session.Pending()) == 0 BEFORE the enqueue
//	queued   — what session.EnqueueEvent returned (a duplicate/stale event is
//	           not an arrival and must not ring)
//	pending  — len(session.Pending()) AFTER the enqueue
func (d *Doorbell) RingOnEdge(wasEmpty, queued bool, pending int) {
	if wasEmpty && queued {
		d.Ring(pending)
	}
}

func (d *Doorbell) ring(pending int) error {
	if err := ensureDoorbellDir(filepath.Dir(d.Path)); err != nil {
		return err
	}
	line := fmt.Sprintf("%s pid=%d pending=%d\n", isoTimestamp(d.now()), d.pid, pending)

	// Truncating rather than appending past the cap keeps an unattended
	// week-long session from growing a log nobody reads. Nothing is lost: the
	// events are on the socket, not here.
	flags := os.O_WRONLY | os.O_CREATE | os.O_APPEND
	if info, err := os.Stat(d.Path); err == nil && info.Size() >= maxMarkerBytes {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(d.Path, flags, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(line); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// isoTimestamp matches JavaScript's Date#toISOString exactly: UTC, always three
// fractional-second digits, a literal trailing Z.
func isoTimestamp(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000") + "Z"
}

// ensureDoorbellDir creates the directory and makes it ignore itself.
//
// A `.gitignore` containing `*` ignores everything in its own directory,
// including itself — so the doorbell never shows up in `git status` and we
// never have to edit a `.gitignore` the user owns. Silently rewriting a tracked
// file in someone's repository to make our own artifact tidy is not ours to do.
//
// O_EXCL is Node's `wx` flag: create only if absent, so a `.gitignore` already
// in that directory (a user's own, or ours from an earlier run) is never
// touched.
func ensureDoorbellDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, ".gitignore"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		// Already there (or unwritable) — either way, not worth failing over.
		return nil
	}
	_, _ = f.WriteString("*\n")
	return f.Close()
}

// ClearDoorbell truncates the marker after a hook has consumed what it
// announced. Idempotent and safe from any process: see the note above about why
// nothing is lost.
func ClearDoorbell(cwd string) {
	_ = os.Truncate(DoorbellPath(cwd), 0)
}
