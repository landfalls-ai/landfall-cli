package hooks

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func fixedClock(iso string) func() time.Time {
	t, _ := time.Parse(time.RFC3339Nano, iso)
	return func() time.Time { return t }
}

func TestDoorbellWritesAContentFreeMarkerLine(t *testing.T) {
	root := t.TempDir()
	d := NewDoorbell(DoorbellOptions{Cwd: root, PID: 4242, Now: fixedClock("2026-08-23T10:11:12.345Z")})
	d.Ring(3)

	body, err := os.ReadFile(DoorbellPath(root))
	if err != nil {
		t.Fatal(err)
	}
	// A timestamp, a pid and a count — never an event, never a finding, never a
	// name. The incident stays on the socket, out of a directory that gets
	// grepped, backed up and occasionally committed.
	if string(body) != "2026-08-23T10:11:12.345Z pid=4242 pending=3\n" {
		t.Fatalf("got %q", body)
	}
}

func TestDoorbellTimestampMatchesJavaScriptToISOString(t *testing.T) {
	// Always UTC, always exactly three fractional digits, a literal trailing Z.
	got := isoTimestamp(time.Date(2026, 8, 23, 10, 11, 12, 7_000_000, time.FixedZone("x", 3600)))
	if got != "2026-08-23T09:11:12.007Z" {
		t.Fatalf("got %q", got)
	}
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`).MatchString(got) {
		t.Fatalf("%q is not an ISO-8601 instant with millisecond precision", got)
	}
}

func TestDoorbellDirectoryIgnoresItself(t *testing.T) {
	root := t.TempDir()
	NewDoorbell(DoorbellOptions{Cwd: root}).Ring(1)

	// `*` ignores everything in its own directory, INCLUDING itself — so the
	// doorbell never shows up in `git status` and we never have to edit a
	// .gitignore the user owns.
	body, err := os.ReadFile(filepath.Join(root, DoorbellDir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "*\n" {
		t.Fatalf("got %q", body)
	}
}

func TestDoorbellNeverOverwritesAGitignoreItDidNotWrite(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, DoorbellDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mine := "# the user's own file\n!keep-me\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}

	NewDoorbell(DoorbellOptions{Cwd: root}).Ring(1)

	body, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	// Silently rewriting a tracked file in someone's repository to make our own
	// artifact tidy is not ours to do.
	if string(body) != mine {
		t.Fatalf("got %q, want the user's file untouched", body)
	}
}

func TestDoorbellAppendsUntilTheCapThenTruncates(t *testing.T) {
	root := t.TempDir()
	d := NewDoorbell(DoorbellOptions{Cwd: root, PID: 1, Now: fixedClock("2026-08-23T10:11:12.345Z")})

	d.Ring(1)
	d.Ring(2)
	body, err := os.ReadFile(d.Path)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(body), "\n"); n != 2 {
		t.Fatalf("want 2 appended lines, got %d", n)
	}

	// Past the 8KB cap the file is rewritten rather than appended to: an
	// unattended week-long session must not grow a log nobody reads. Nothing is
	// lost — the events are on the socket, not here.
	if err := os.WriteFile(d.Path, make([]byte, maxMarkerBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	d.Ring(9)
	body, err = os.ReadFile(d.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "2026-08-23T10:11:12.345Z pid=1 pending=9\n" {
		t.Fatalf("got %d bytes: %q", len(body), body)
	}
}

func TestRingOnEdgeOnlyRingsOnTheZeroToNonEmptyTransition(t *testing.T) {
	root := t.TempDir()
	d := NewDoorbell(DoorbellOptions{Cwd: root, PID: 1, Now: fixedClock("2026-08-23T10:11:12.345Z")})

	d.RingOnEdge(true, true, 1)   // the edge: rings
	d.RingOnEdge(false, true, 2)  // already owed context: a busy room must not re-ring
	d.RingOnEdge(true, false, 0)  // a duplicate/stale event is not an arrival
	d.RingOnEdge(false, false, 0) // neither

	body, err := os.ReadFile(d.Path)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(body), "\n"); n != 1 {
		t.Fatalf("want exactly 1 ring, got %d: %q", n, body)
	}
}

func TestRingIsBestEffortAndWarnsExactlyOncePerProcess(t *testing.T) {
	// A read-only checkout must not take down a serve process; it just means an
	// idle session finds out at its next tool call instead.
	root := t.TempDir()
	blocked := filepath.Join(root, "blocked")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var warnings []string
	// A regular file where the .landfall directory should go: MkdirAll fails.
	d := NewDoorbell(DoorbellOptions{Cwd: blocked, Log: func(msg string) { warnings = append(warnings, msg) }})

	d.Ring(1)
	d.Ring(2)
	d.Ring(3)

	if len(warnings) != 1 {
		t.Fatalf("want one warning per process, got %d: %v", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0], "doorbell unavailable") {
		t.Fatalf("got %q", warnings[0])
	}
}

func TestClearDoorbellTruncatesAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	d := NewDoorbell(DoorbellOptions{Cwd: root})
	d.Ring(1)

	ClearDoorbell(root)
	info, err := os.Stat(d.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Fatalf("size %d, want 0", info.Size())
	}
	ClearDoorbell(root)        // idempotent
	ClearDoorbell(t.TempDir()) // and safe where nothing was ever written
}

func TestDoorbellFilenameStaysOnClaudeCodesExactMatchPath(t *testing.T) {
	// Claude Code's FileChanged matcher exact-matches on letters, digits, `_`
	// and `|`. A hyphen would drop the whole matcher onto the regex path, so the
	// underscore is load-bearing rather than stylistic.
	if DoorbellFile != "room_events" {
		t.Fatalf("DoorbellFile = %q", DoorbellFile)
	}
	if strings.ContainsAny(DoorbellFile, "-./") {
		t.Fatalf("%q must be a bare, exact-matchable filename", DoorbellFile)
	}
	if MatcherFor("file-changed") != DoorbellFile {
		t.Fatal("the registered matcher must be the bare filename")
	}
}
