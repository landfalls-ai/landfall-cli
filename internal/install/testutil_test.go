package install

// testutil_test.go — the sandboxed "machine that is not the one running the
// tests", ported from `test/install/helpers.mjs`.
//
// Every adapter test needs two things: a HOME that is not a developer's own
// (so we never touch a real ~/.cursor/mcp.json), and, for the harnesses driven
// by shelling out to their own CLI, a way to observe what was invoked without
// the real `claude`/`codex`/`code` binaries being installed.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type sandbox struct {
	root    string
	homeDir string
	pathDir string
	appRoot string
}

// newSandbox redirects HOME/USERPROFILE/APPDATA/XDG_CONFIG_HOME into a fresh
// temp tree, replaces PATH with a stub directory plus the minimal system PATH,
// and redirects app-bundle detection there too.
//
// PATH is REPLACED, not prepended to: a harness genuinely installed on the
// machine running the tests (this very `claude` binary, for instance) would
// otherwise leak into Detect() and make the suite's result depend on whose
// laptop runs it.
func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	root := t.TempDir()
	s := &sandbox{
		root:    root,
		homeDir: filepath.Join(root, "home"),
		pathDir: filepath.Join(root, "bin"),
		appRoot: filepath.Join(root, "Applications"),
	}
	for _, d := range []string{s.homeDir, s.pathDir, s.appRoot} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", s.homeDir)
	t.Setenv("USERPROFILE", s.homeDir)
	t.Setenv("APPDATA", filepath.Join(s.homeDir, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(s.homeDir, "AppData", "Local"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(s.homeDir, ".config"))
	t.Setenv("PATH", strings.Join(append([]string{s.pathDir}, "/usr/bin", "/bin", "/usr/sbin", "/sbin"), string(os.PathListSeparator)))
	t.Setenv("LANDFALL_TEST_APP_ROOT", s.appRoot)
	return s
}

func (s *sandbox) path(parts ...string) string {
	return filepath.Join(append([]string{s.homeDir}, parts...)...)
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	blob, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(blob, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// readJSONFile parses a file, or returns nil if it does not exist.
func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return out
}

func readText(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// stubOnPath drops an inert executable named `name` onto the sandbox's PATH
// dir, so IsOnPath(name) answers true without the real binary existing.
func (s *sandbox) stubOnPath(t *testing.T, name string) {
	t.Helper()
	p := filepath.Join(s.pathDir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// recorder swaps package-level execCommand so a test can see every subprocess
// argv without any real CLI being installed, and can fail selected calls.
type recorder struct {
	calls [][]string
	// failIf, when non-nil, decides per-argv whether the call exits non-zero.
	failIf func(argv []string) bool
}

func (r *recorder) hook(t *testing.T) {
	t.Helper()
	prev := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		argv := append([]string{name}, args...)
		r.calls = append(r.calls, argv)
		code := "0"
		if r.failIf != nil && r.failIf(argv) {
			code = "1"
		}
		return exec.Command("/bin/sh", "-c", "exit "+code)
	}
	t.Cleanup(func() { execCommand = prev })
}

func (r *recorder) withPrefix(prefix ...string) [][]string {
	var out [][]string
	for _, c := range r.calls {
		if len(c) < len(prefix) {
			continue
		}
		match := true
		for i, p := range prefix {
			if c[i] != p {
				match = false
				break
			}
		}
		if match {
			out = append(out, c)
		}
	}
	return out
}
