package cli

// root_test.go — the argv grammar of bin/landfall.mjs's parseArgs (lines
// 81-89), which Cobra has no native equivalent for and which therefore has to
// be asserted directly rather than assumed.

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func noEnv(string) string { return "" }

func TestParseArgs(t *testing.T) {
	const shareLink = "https://landfall.test/o/acme/incidents/inc-1/agent?ticket=abc"

	cases := []struct {
		name     string
		argv     []string
		getenv   func(string) string
		wantCmd  string
		wantLink string
		wantRest []string
	}{
		{
			name:    "no arguments defaults to serve",
			argv:    nil,
			wantCmd: "serve",
		},
		{
			name:     "a bare URL is serve with that link",
			argv:     []string{shareLink},
			wantCmd:  "serve",
			wantLink: shareLink,
		},
		{
			name:     "a positional URL after a command becomes the link",
			argv:     []string{"join", shareLink},
			wantCmd:  "join",
			wantLink: shareLink,
		},
		{
			name:     "--link is spliced out wherever it appears",
			argv:     []string{"install", "--link", shareLink, "--yes"},
			wantCmd:  "install",
			wantLink: shareLink,
			wantRest: []string{"--yes"},
		},
		{
			name:     "--link wins over a positional URL, which stays in rest",
			argv:     []string{"join", "--link", shareLink, "https://other.test/x"},
			wantCmd:  "join",
			wantLink: shareLink,
			wantRest: []string{"https://other.test/x"},
		},
		{
			name:    "a trailing --link with no value removes only itself",
			argv:    []string{"note", "hello", "--link"},
			wantCmd: "note",
			// LANDFALL_LINK is the fallback once --link supplied no value.
			getenv:   func(k string) string { return map[string]string{"LANDFALL_LINK": shareLink}[k] },
			wantLink: shareLink,
			wantRest: []string{"hello"},
		},
		{
			name:     "LANDFALL_LINK is the fallback when nothing else supplied one",
			argv:     []string{"note", "hello"},
			getenv:   func(k string) string { return map[string]string{"LANDFALL_LINK": shareLink}[k] },
			wantCmd:  "note",
			wantLink: shareLink,
			wantRest: []string{"hello"},
		},
		{
			name: "an explicitly empty --link is not replaced by the environment",
			argv: []string{"join", "--link", ""},
			getenv: func(k string) string {
				return map[string]string{"LANDFALL_LINK": shareLink}[k]
			},
			wantCmd:  "join",
			wantLink: "",
		},
		{
			name:     "note keeps its whole text in rest",
			argv:     []string{"note", "redis", "evictions", "spiked"},
			wantCmd:  "note",
			wantRest: []string{"redis", "evictions", "spiked"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			getenv := tc.getenv
			if getenv == nil {
				getenv = noEnv
			}
			cmd, link, rest := parseArgs(tc.argv, getenv)
			if cmd != tc.wantCmd {
				t.Errorf("cmd = %q, want %q", cmd, tc.wantCmd)
			}
			if link != tc.wantLink {
				t.Errorf("link = %q, want %q", link, tc.wantLink)
			}
			if len(rest) != 0 || len(tc.wantRest) != 0 {
				if !reflect.DeepEqual(rest, tc.wantRest) {
					t.Errorf("rest = %q, want %q", rest, tc.wantRest)
				}
			}
		})
	}
}

func TestWantsHelp(t *testing.T) {
	yes := [][]string{
		{"--help"},
		{"-h"},
		{"help"},
		{"note", "--help"},
		{"connect", "aws", "--help"},
		{"note", "--", "-h"}, // matched anywhere, and `--` is not a terminator here
	}
	for _, argv := range yes {
		if !wantsHelp(argv) {
			t.Errorf("wantsHelp(%q) = false, want true", argv)
		}
	}

	no := [][]string{
		nil,
		{"serve"},
		{"note", "help"}, // `help` only counts as argv[0]
		{"note", "--helpme"},
	}
	for _, argv := range no {
		if wantsHelp(argv) {
			t.Errorf("wantsHelp(%q) = true, want false", argv)
		}
	}
}

// An unrecognized command name must reach `serve`, not a Cobra "unknown
// command" error: today's dispatcher is a chain of `if (cmd === …)` checks
// that falls through to serve (bin/landfall.mjs:441).
func TestUnknownCommandRoutesToServe(t *testing.T) {
	root := newRootCommand(New(), "")
	for _, name := range []string{"serve", "join", "note", "leave", "login", "logout", "instance"} {
		if !hasCommand(root, name) {
			t.Errorf("hasCommand(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"bogus", "help", ""} {
		if hasCommand(root, name) {
			t.Errorf("hasCommand(%q) = true, want false (it must fall through to serve)", name)
		}
	}
}

// TestNoCommandIsAPlaceholder is the regression test for a real gap this
// port hit: status, connect, and remediation each had a complete, tested
// Run*/Parse* implementation for a while before their Cobra constructors
// were actually added to root.go's AddCommand — every existing test called
// the exported functions directly, so `go test ./...` stayed green the
// whole time while the built binary silently answered "not implemented" for
// all three. Only running the real binary end to end caught it. This test
// encodes the fix at the cheapest possible layer: placeholderCommands must
// stay empty, because a name added there without also being wired into
// root.go is exactly how the gap happened.
func TestNoCommandIsAPlaceholder(t *testing.T) {
	if got := placeholderCommands(New()); len(got) != 0 {
		names := make([]string, len(got))
		for i, c := range got {
			names[i] = c.Use
		}
		t.Errorf("placeholderCommands returned %v — every one of these is unreachable from the built binary "+
			"even if its Run/Parse functions are fully implemented and tested; wire it into root.go's AddCommand", names)
	}
}

// TestEveryRegisteredCommandRunsWithoutClaimingToBeUnimplemented actually
// executes a subset of top-level commands and asserts the specific
// placeholder wording never appears — a second, independent check on the
// same failure mode via the real Execute path rather than reflection over
// the command tree. Deliberately excludes anything with a real-world side
// effect this test suite must never trigger: serve/join (block on I/O or a
// real connection); login and install (both can reach auth.Login's real
// browser-open + 5-minute poll when no session is cached — install's own
// test file already covers it properly with a seeded session; found this
// one the hard way too, by it hanging the suite a second time).
//
// XDG_RUNTIME_DIR/HOME are sandboxed to a temp dir for the same reason: the
// commands under test (status in particular) resolve their own hook-socket
// runtime dir from the REAL environment regardless of what's passed to
// run()'s getenv param — that param only reaches instance resolution.
// Running this unsandboxed on a dev machine that happens to have OTHER real
// landfall serve processes running is how this test hung the first time:
// status genuinely tried to query real, unrelated sockets on this machine.
func TestEveryRegisteredCommandRunsWithoutClaimingToBeUnimplemented(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", tmp)
	t.Setenv("HOME", tmp)
	for _, name := range []string{"status", "connect", "remediation", "uninstall", "hooks", "instance", "logout", "note", "leave"} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			ui := &UI{Out: &out, Err: &out}
			exitCode := run(ui, []string{name}, noEnv)
			if strings.Contains(out.String(), "is not implemented in the Go build yet") {
				t.Errorf("landfall %s claims to be unimplemented (exit %d): %s", name, exitCode, out.String())
			}
		})
	}
}
