package cli_test

// help_test.go — a port of test/help.test.mjs.
//
// v0.1.1 fix, kept as a regression test: `landfall --help`/`-h`/`help` must
// print usage and exit 0 instead of falling through to `serve` (which would
// hang waiting on an MCP handshake). Found while writing homebrew-landfall's
// Formula test block (specs/050-extract-cli-homebrew).
//
// Like the Node original, this spawns the REAL built binary rather than
// calling Execute in-process. That is the point of the test: the failure it
// guards against is a routing decision made before any exported function is
// reached, and only a real argv → real process → real exit code can prove the
// three spellings still take that path.

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
)

// binPath is the built binary every case below spawns, compiled once in
// TestMain.
var binPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "landfall-cli-test")
	if err != nil {
		panic("creating a temp dir for the test binary: " + err.Error())
	}
	defer func() { _ = os.RemoveAll(dir) }()

	binPath = filepath.Join(dir, "landfall")
	// The import path, not a relative one: `go test` runs with the package
	// directory as its working directory, so "./cmd/landfall" would not resolve.
	build := exec.Command("go", "build", "-o", binPath, "github.com/landfalls-ai/landfall-cli/cmd/landfall")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		panic("building the test binary: " + err.Error())
	}

	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// run spawns the binary with args and returns its stdout and exit code, with
// stdout and stderr captured SEPARATELY — the usage text is asserted on
// stdout specifically (contracts/cli-commands.md's Global conventions), so a
// combined capture would let a regression that moved it to stderr pass.
func run(t *testing.T, args ...string) (stdout string, exitCode int) {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("running %v: %v", args, err)
		}
		exitCode = exit.ExitCode()
	}
	return out.String(), exitCode
}

var (
	usageLine   = regexp.MustCompile(`Usage: landfall <command>`)
	installLine = regexp.MustCompile(`install \[--yes\]`) // feature 049's commands are documented too
)

func TestHelpSpellingsPrintUsageAndExitZero(t *testing.T) {
	for _, spelling := range []string{"--help", "-h", "help"} {
		t.Run(spelling, func(t *testing.T) {
			stdout, exitCode := run(t, spelling)
			if exitCode != 0 {
				t.Errorf("landfall %s exited %d, want 0", spelling, exitCode)
			}
			if !usageLine.MatchString(stdout) {
				t.Errorf("landfall %s stdout does not match %s:\n%s", spelling, usageLine, stdout)
			}
			if !installLine.MatchString(stdout) {
				t.Errorf("landfall %s stdout does not match %s:\n%s", spelling, installLine, stdout)
			}
		})
	}
}

// The parity traps the Node original never had to state, because a hand-rolled
// argv scan has no other behavior available to it. Cobra does, so each is
// asserted against the real binary rather than trusted.
func TestHelpIsMatchedAnywhereInArgv(t *testing.T) {
	cases := [][]string{
		{"note", "--help"},           // a command that would otherwise consume it as text
		{"connect", "aws", "--help"}, // must be the TOP-LEVEL help, never a per-command one
		{"install", "-h"},
		{"--link", "https://example.test/o/a/incidents/b/agent?ticket=t", "--help"},
	}
	for _, args := range cases {
		stdout, exitCode := run(t, args...)
		if exitCode != 0 {
			t.Errorf("landfall %v exited %d, want 0", args, exitCode)
		}
		if !usageLine.MatchString(stdout) || !installLine.MatchString(stdout) {
			t.Errorf("landfall %v did not print the top-level usage text:\n%s", args, stdout)
		}
	}
}
