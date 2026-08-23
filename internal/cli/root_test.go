package cli

// root_test.go — the argv grammar of bin/landfall.mjs's parseArgs (lines
// 81-89), which Cobra has no native equivalent for and which therefore has to
// be asserted directly rather than assumed.

import (
	"reflect"
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
