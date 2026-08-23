package cli

// hooksinstall_test.go — `landfall hooks install` / `landfall hooks
// uninstall` (T052), ported from test/hooks/hooks-cli.test.mjs (7 cases) and
// the CLI half of test/hooks/idempotent-and-nondestructive.test.mjs (8 cases).
//
// The idempotent-and-nondestructive suite is the one these two commands are
// actually judged on: "repeated installs are no-ops" and "existing user hooks
// are never clobbered". Real files in a sandboxed HOME throughout — the point
// is what the config file on disk looks like afterwards.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var hookHostOrder = []string{"Claude Code", "Codex CLI", "Cursor"}

// claudeSettings makes ~/.claude (which is what Detect() looks for) and
// returns the settings path.
func claudeSettings(t *testing.T, home string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(home, ".claude", "settings.json")
}

func hooksDeps() HooksDeps {
	// `landfall` resolvable, so the PATH warning stays out of the assertions.
	return HooksDeps{IsOnPath: func(string) bool { return true }}
}

func readJSON(t *testing.T, path string) map[string]any {
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

func readFileText(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// --- contract ------------------------------------------------------------

func TestHooksInstall_CleanMachineEveryHostNotDetectedExitZero(t *testing.T) {
	installSandbox(t)
	r := RunHooksInstall(context.Background(), []string{"install"}, hooksDeps())
	if r.ExitCode != 0 {
		t.Fatalf("exit %d", r.ExitCode)
	}
	want := []string{"Claude Code: not-detected", "Codex CLI: not-detected", "Cursor: not-detected"}
	if !reflect.DeepEqual(statusLines(r), want) {
		t.Fatalf("got %v want %v", statusLines(r), want)
	}
	if !reflect.DeepEqual(displayNames(r), hookHostOrder) {
		t.Fatalf("got %v", displayNames(r))
	}
}

func TestHooksInstall_NeedsNoSignIn(t *testing.T) {
	// No session is seeded anywhere in this file, deliberately: unlike
	// `landfall install`, editing local hook config is not an authenticated
	// action, and it has to work from a fleet provisioning script.
	home := installSandbox(t)
	if err := os.MkdirAll(filepath.Join(home, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := RunHooksInstall(context.Background(), []string{"install", "--only", "cursor"}, hooksDeps())
	if r.ExitCode != 0 {
		t.Fatalf("exit %d", r.ExitCode)
	}
	if !strings.HasPrefix(statusLines(r)[0], "Cursor: configured ") {
		t.Fatalf("got %q", statusLines(r)[0])
	}
}

func TestHooksInstall_UnknownOnlyIsAUsageErrorExitTwoAndWritesNothing(t *testing.T) {
	home := installSandbox(t)
	if err := os.MkdirAll(filepath.Join(home, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := RunHooksInstall(context.Background(), []string{"install", "--only", "not-a-host"}, hooksDeps())
	if r.ExitCode != 2 {
		t.Fatalf("exit %d", r.ExitCode)
	}
	if _, err := os.Stat(filepath.Join(home, ".cursor", "hooks.json")); err == nil {
		t.Fatal("a usage error must write nothing")
	}
}

func TestHooksInstall_DryRunReportsWouldConfigureAndWritesNothing(t *testing.T) {
	home := installSandbox(t)
	settings := claudeSettings(t, home)

	r := RunHooksInstall(context.Background(), []string{"install", "--only", "claude-code", "--dry-run"}, hooksDeps())
	if r.ExitCode != 0 {
		t.Fatalf("exit %d", r.ExitCode)
	}
	if got := statusLines(r)[0]; got != "Claude Code: would-configure ("+settings+")" {
		t.Fatalf("got %q", got)
	}
	if _, err := os.Stat(settings); err == nil {
		t.Fatal("a dry run must write nothing")
	}
}

func TestHooks_UsageLineNamesEverySubcommandAndEveryEvent(t *testing.T) {
	got := hooksUsage()
	for _, want := range []string{"install", "uninstall", "policy", "stop", "file-changed", "user-prompt-submit", "pre-tool-use"} {
		if !strings.Contains(got, want) {
			t.Fatalf("usage %q must mention %q", got, want)
		}
	}
	if !strings.HasPrefix(got, "usage: landfall hooks <install|uninstall|policy|") {
		t.Fatalf("got %q", got)
	}
}

// --- idempotency and non-destructiveness --------------------------------

func TestHooksInstall_RunTwiceTheSecondIsAlreadyInstalledAndTheFileIsByteIdentical(t *testing.T) {
	home := installSandbox(t)
	settings := claudeSettings(t, home)

	first := RunHooksInstall(context.Background(), []string{"install", "--only", "claude-code"}, hooksDeps())
	if got := statusLines(first)[0]; got != "Claude Code: configured ("+settings+")" {
		t.Fatalf("got %q", got)
	}
	afterFirst := readFileText(t, settings)

	second := RunHooksInstall(context.Background(), []string{"install", "--only", "claude-code"}, hooksDeps())
	if got := statusLines(second)[0]; got != "Claude Code: already-installed ("+settings+")" {
		t.Fatalf("got %q", got)
	}
	if readFileText(t, settings) != afterFirst {
		t.Fatal("the second run must not rewrite the file")
	}

	// And not merely "no second write" — no duplicate entry either.
	config := readJSON(t, settings)["hooks"].(map[string]any)
	for _, event := range []string{"Stop", "FileChanged"} {
		if n := len(config[event].([]any)); n != 1 {
			t.Fatalf("%s has %d entries, want 1", event, n)
		}
	}
}

func TestHooksInstall_AUserHookOnTheSameEventSurvivesInstallAndUninstall(t *testing.T) {
	home := installSandbox(t)
	settings := claudeSettings(t, home)
	userHook := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "my-own-script.sh"}}}
	blob, _ := json.Marshal(map[string]any{"model": "opus", "hooks": map[string]any{"Stop": []any{userHook}}})
	if err := os.WriteFile(settings, blob, 0o644); err != nil {
		t.Fatal(err)
	}

	RunHooksInstall(context.Background(), []string{"install", "--only", "claude-code"}, hooksDeps())
	installed := readJSON(t, settings)
	if installed["model"] != "opus" {
		t.Fatal("unrelated settings preserved")
	}
	stop := installed["hooks"].(map[string]any)["Stop"].([]any)
	if len(stop) != 2 {
		t.Fatalf("want 2 entries, got %d", len(stop))
	}
	if !reflect.DeepEqual(stop[0], userHook) {
		t.Fatalf("the user hook must still be first: %v", stop[0])
	}
	if got := stop[1].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"]; got != "landfall hooks stop" {
		t.Fatalf("got %v", got)
	}

	r := RunHooksUninstall(context.Background(), []string{"uninstall", "--only", "claude-code"}, hooksDeps())
	if got := statusLines(r)[0]; got != "Claude Code: removed ("+settings+")" {
		t.Fatalf("got %q", got)
	}
	after := readJSON(t, settings)
	if got := after["hooks"].(map[string]any)["Stop"].([]any); len(got) != 1 || !reflect.DeepEqual(got[0], userHook) {
		t.Fatalf("ours gone, theirs untouched: %v", got)
	}
	if after["model"] != "opus" {
		t.Fatal("unrelated settings preserved")
	}
	if _, still := after["hooks"].(map[string]any)["FileChanged"]; still {
		t.Fatal("a list we emptied is pruned, not left as []")
	}
}

func TestHooksInstallThenUninstall_LeavesNoLandfallResidue(t *testing.T) {
	home := installSandbox(t)
	settings := claudeSettings(t, home)
	RunHooksInstall(context.Background(), []string{"install", "--only", "claude-code"}, hooksDeps())
	RunHooksUninstall(context.Background(), []string{"uninstall", "--only", "claude-code"}, hooksDeps())
	if got := readJSON(t, settings); len(got) != 0 {
		t.Fatalf("the empty hooks container is pruned too, got %v", got)
	}
}

func TestHooksInstall_AHandEditedEntryIsAConflictNeverOverwrittenNeverDuplicated(t *testing.T) {
	home := installSandbox(t)
	settings := claudeSettings(t, home)
	if err := os.WriteFile(settings, []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"landfall hooks stop --verbose"}]}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	before := readFileText(t, settings)

	r := RunHooksInstall(context.Background(), []string{"install", "--only", "claude-code"}, hooksDeps())
	if got := statusLines(r)[0]; !strings.HasPrefix(got, "Claude Code: conflict — an existing landfall hook entry differs") {
		t.Fatalf("got %q", got)
	}
	if r.ExitCode != 0 {
		t.Fatalf("a conflict is a report, not a failure: exit %d", r.ExitCode)
	}
	if readFileText(t, settings) != before {
		t.Fatal("not one byte written")
	}
}

func TestHooksUninstall_LeavesAHandEditedEntryInPlaceRatherThanDeletingIt(t *testing.T) {
	home := installSandbox(t)
	settings := claudeSettings(t, home)
	const before = `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"landfall hooks stop --verbose"}]}]}}`
	if err := os.WriteFile(settings, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}

	r := RunHooksUninstall(context.Background(), []string{"uninstall", "--only", "claude-code"}, hooksDeps())
	if got := statusLines(r)[0]; got != "Claude Code: left-in-place ("+settings+")" {
		t.Fatalf("got %q", got)
	}
	if readFileText(t, settings) != before {
		t.Fatal("a hand-edited entry is not ours to delete")
	}
}

func TestHooksInstall_AnUnparseableConfigIsReportedNeverOverwritten(t *testing.T) {
	home := installSandbox(t)
	settings := claudeSettings(t, home)
	const original = "{ this is not json"
	if err := os.WriteFile(settings, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	r := RunHooksInstall(context.Background(), []string{"install", "--only", "claude-code"}, hooksDeps())
	line := statusLines(r)[0]
	if !strings.HasPrefix(line, "Claude Code: failed — ") || !strings.Contains(line, "could not be parsed as JSON") {
		t.Fatalf("got %q", line)
	}
	if r.ExitCode != 1 {
		t.Fatalf("exit %d", r.ExitCode)
	}
	if readFileText(t, settings) != original {
		t.Fatal("never overwritten")
	}
}

func TestHooksUninstall_OnAMachineThatWasNeverInstalledIsNotInstalledExitZero(t *testing.T) {
	home := installSandbox(t)
	claudeSettings(t, home)
	r := RunHooksUninstall(context.Background(), []string{"uninstall", "--only", "claude-code"}, hooksDeps())
	if !strings.HasPrefix(statusLines(r)[0], "Claude Code: not-installed ") {
		t.Fatalf("got %q", statusLines(r)[0])
	}
	if r.ExitCode != 0 {
		t.Fatalf("exit %d", r.ExitCode)
	}
}

func TestHooksInstall_TheUninstallFlagIsTheFlagFormOfHooksUninstall(t *testing.T) {
	home := installSandbox(t)
	settings := claudeSettings(t, home)
	RunHooksInstall(context.Background(), []string{"install", "--only", "claude-code"}, hooksDeps())

	// `hooks install --uninstall` must route to the uninstall body — the flag
	// form the ticket specifies.
	f := parseHookFlags([]string{"install", "--only", "claude-code", "--uninstall"})
	if !f.uninstall || f.rest[0] != "install" {
		t.Fatalf("got %+v", f)
	}
	r := RunHooksUninstall(context.Background(), []string{"install", "--only", "claude-code", "--uninstall"}, hooksDeps())
	if got := statusLines(r)[0]; got != "Claude Code: removed ("+settings+")" {
		t.Fatalf("got %q", got)
	}
	if got := readJSON(t, settings); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestHooksInstall_WarnsWhenLandfallIsNotResolvableOnPath(t *testing.T) {
	home := installSandbox(t)
	claudeSettings(t, home)
	var logged []string
	RunHooksInstall(context.Background(), []string{"install", "--only", "claude-code"}, HooksDeps{
		IsOnPath: func(string) bool { return false },
		Log:      func(f string, a ...any) { logged = append(logged, f) },
	})
	if !anyContains(logged, "not resolvable on PATH") {
		t.Fatalf("a hook whose command does not resolve fires and fails on every turn: %v", logged)
	}
}

// --- flag parsing --------------------------------------------------------

func TestParseHookFlags_KeepsTheSubcommandFirstWhereverTheFlagsAppear(t *testing.T) {
	for _, argv := range [][]string{
		{"install", "--only", "cursor", "--dry-run"},
		{"--dry-run", "install", "--only", "cursor"},
		{"--only", "cursor", "--dry-run", "install"},
	} {
		f := parseHookFlags(argv)
		if len(f.rest) == 0 || f.rest[0] != "install" {
			t.Fatalf("%v -> rest %v", argv, f.rest)
		}
		if !f.dryRun || !reflect.DeepEqual(f.only, []string{"cursor"}) {
			t.Fatalf("%v -> %+v", argv, f)
		}
	}
}

func TestParseHookFlags_HostBelongsToAnInvocationNotToInstall(t *testing.T) {
	f := parseHookFlags([]string{"stop", "--host", "cursor"})
	if f.host != "cursor" {
		t.Fatalf("got %q", f.host)
	}
	if len(f.rest) != 1 || f.rest[0] != "stop" {
		t.Fatalf("got %v", f.rest)
	}
}

func TestParseHookFlags_ATrailingValuelessFlagDoesNotPanic(t *testing.T) {
	if f := parseHookFlags([]string{"stop", "--host"}); f.host != "" || f.rest[0] != "stop" {
		t.Fatalf("got %+v", f)
	}
	if f := parseHookFlags([]string{"install", "--only"}); f.only == nil || len(f.only) != 0 {
		t.Fatalf("got %+v", f)
	}
}
