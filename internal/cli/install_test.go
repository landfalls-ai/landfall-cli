package cli

// install_test.go — `landfall install` / `landfall uninstall` (T051), ported
// from test/install/{install-cli,uninstall-cli,us1-login-gate,us1-none-
// detected,us3-only-flag,us3-selective,us3-yes-flag}.test.mjs.
//
// The command BODIES are exercised directly (RunInstall/RunUninstall) against
// a sandboxed HOME with real files, rather than by spawning a binary: the
// login gate needs a fake session, and the real login flow is interactive and
// network-bound.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/install"
)

var harnessOrder = []string{"Claude Code", "Cursor", "VS Code", "Codex CLI", "Claude Desktop", "Windsurf"}

// installSandbox redirects HOME and REPLACES PATH, so a harness genuinely
// installed on the machine running the suite cannot be detected as if it were
// on the sandboxed one.
func installSandbox(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	binDir := filepath.Join(root, "bin")
	appRoot := filepath.Join(root, "Applications")
	for _, d := range []string{home, binDir, appRoot} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("PATH", strings.Join([]string{binDir, "/usr/bin", "/bin"}, string(os.PathListSeparator)))
	t.Setenv("LANDFALL_TEST_APP_ROOT", appRoot)
	return home
}

// seedSession writes a valid cached session under the sandboxed
// XDG_CONFIG_HOME, so a command going through the REAL dependency defaults
// treats itself as signed in without ever running the real (interactive,
// browser-bound, five-minute-polling) login flow. The Go equivalent of
// test/install/helpers.mjs#seedSession, and mandatory for any tree-level test
// of `install`: without it, RunInstall's default Login is auth.Login and the
// test hangs instead of failing.
func seedSession(t *testing.T) {
	t.Helper()
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "landfall")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(time.Hour).UnixMilli()
	blob, err := json.Marshal(map[string]any{
		"access_token": "test-token",
		"expires_at":   expires,
		"org_slug":     "acme",
		"iss":          "landfall-core",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), blob, 0o600); err != nil {
		t.Fatal(err)
	}
}

// signedIn is the dependency set every test that is not ABOUT the login gate
// uses: a cached session already present, and `landfall` resolvable so the
// PATH warning stays quiet.
func signedIn() InstallDeps {
	return InstallDeps{
		GetCachedAccessToken: func() string { return "test-token" },
		Login:                func(func(string)) error { panic("login must not run") },
		IsOnPath:             func(string) bool { return true },
	}
}

func statusLines(r InstallResult) []string {
	lines := make([]string, 0, len(r.Outcomes))
	for _, o := range r.Outcomes {
		lines = append(lines, install.FormatOutcomeLine(o))
	}
	return lines
}

func displayNames(r InstallResult) []string {
	names := make([]string, 0, len(r.Outcomes))
	for _, o := range r.Outcomes {
		names = append(names, o.DisplayName)
	}
	return names
}

// --- contract: argv, exit codes, output shape ---------------------------

func TestInstall_CleanMachineReportsAllSixNotDetectedExitZero(t *testing.T) {
	installSandbox(t)
	r := RunInstall(context.Background(), []string{"--yes"}, signedIn())
	if r.ExitCode != 0 {
		t.Fatalf("exit %d", r.ExitCode)
	}
	if !reflect.DeepEqual(displayNames(r), harnessOrder) {
		t.Fatalf("got %v want %v", displayNames(r), harnessOrder)
	}
	for _, line := range statusLines(r) {
		if !strings.HasSuffix(line, ": not-detected") {
			t.Fatalf("got %q", line)
		}
	}
}

func TestInstall_TouchesNothingUnderHomeOnACleanMachine(t *testing.T) {
	home := installSandbox(t)
	before := treeUnder(t, home)

	RunInstall(context.Background(), []string{"--yes"}, signedIn())

	if got := treeUnder(t, home); !reflect.DeepEqual(got, before) {
		t.Fatalf("install created files it had no business creating:\n got %v\nwas %v", got, before)
	}
	for _, p := range []string{".cursor/mcp.json", ".claude.json", ".codex/config.toml", ".codeium/windsurf/mcp_config.json"} {
		if _, err := os.Stat(filepath.Join(home, filepath.FromSlash(p))); err == nil {
			t.Fatalf("%s must not exist", p)
		}
	}
}

func TestInstall_UnknownOnlyIDIsAUsageErrorExitTwoAndNeedsNoSession(t *testing.T) {
	installSandbox(t)
	r := RunInstall(context.Background(), []string{"--only", "not-a-real-harness"}, InstallDeps{
		// Deliberately no token and a login that would fail: the --only check
		// runs FIRST, so this must never reach the gate.
		GetCachedAccessToken: func() string { return "" },
		Login:                func(func(string)) error { panic("login must not run") },
	})
	if r.ExitCode != 2 {
		t.Fatalf("exit %d", r.ExitCode)
	}
	if !strings.Contains(r.UsageError, "not-a-real-harness") {
		t.Fatalf("the message must name the bad id, got %q", r.UsageError)
	}
	if len(r.Outcomes) != 0 {
		t.Fatal("nothing was inspected, let alone written")
	}
}

func TestUninstall_CleanMachineReportsAllSixNotInstalledExitZeroWithNoSession(t *testing.T) {
	installSandbox(t)
	r := RunUninstall(context.Background(), []string{"--yes"}, UninstallDeps{})
	if r.ExitCode != 0 {
		t.Fatalf("exit %d", r.ExitCode)
	}
	if !reflect.DeepEqual(displayNames(r), harnessOrder) {
		t.Fatalf("got %v", displayNames(r))
	}
	for _, line := range statusLines(r) {
		if !strings.HasSuffix(line, ": not-installed") {
			t.Fatalf("got %q", line)
		}
	}
}

func TestUninstall_UnknownOnlyIDIsAUsageErrorExitTwo(t *testing.T) {
	installSandbox(t)
	r := RunUninstall(context.Background(), []string{"--only", "not-a-real-harness"}, UninstallDeps{})
	if r.ExitCode != 2 {
		t.Fatalf("exit %d", r.ExitCode)
	}
}

// --- the login gate ------------------------------------------------------

type fakeHarness struct {
	id, name string
	detect   func() bool
	install  func() install.Outcome
	hasEntry func() bool
	remove   func() install.Outcome
}

func (f *fakeHarness) ID() string          { return f.id }
func (f *fakeHarness) DisplayName() string { return f.name }
func (f *fakeHarness) Detect() bool {
	if f.detect != nil {
		return f.detect()
	}
	return true
}
func (f *fakeHarness) Install() install.Outcome {
	if f.install != nil {
		return f.install()
	}
	return install.Outcome{Status: "configured"}
}
func (f *fakeHarness) HasEntry() bool {
	if f.hasEntry != nil {
		return f.hasEntry()
	}
	return true
}
func (f *fakeHarness) Uninstall() install.Outcome {
	if f.remove != nil {
		return f.remove()
	}
	return install.Outcome{Status: "removed"}
}

func TestInstall_TriggersLoginBEFOREDetectingAnyHarnessWhenNoTokenIsCached(t *testing.T) {
	var calls []string
	signedInNow := false
	h := &fakeHarness{id: "h1", name: "H1", detect: func() bool { calls = append(calls, "detect"); return true }}

	r := RunInstall(context.Background(), nil, InstallDeps{
		Harnesses: []install.Harness{h},
		Login: func(func(string)) error {
			calls = append(calls, "login")
			signedInNow = true
			return nil
		},
		GetCachedAccessToken: func() string {
			if signedInNow {
				return "tok"
			}
			return ""
		},
		PromptSelection: func(items []install.SelectItem, _ bool) []string { return idsOf(items) },
		IsOnPath:        func(string) bool { return true },
	})

	// The ORDER is the point: `landfall install` registers the user's own
	// machine, so the session is a precondition of the whole command.
	if !reflect.DeepEqual(calls, []string{"login", "detect"}) {
		t.Fatalf("got %v", calls)
	}
	if r.Outcomes[0].Status != "configured" {
		t.Fatalf("got %+v", r.Outcomes[0])
	}
}

func TestInstall_DoesNotCallLoginWhenASessionIsAlreadyCached(t *testing.T) {
	var calls []string
	RunInstall(context.Background(), nil, InstallDeps{
		Harnesses:            []install.Harness{&fakeHarness{id: "h1", name: "H1"}},
		Login:                func(func(string)) error { calls = append(calls, "login"); return nil },
		GetCachedAccessToken: func() string { return "already-signed-in" },
		PromptSelection:      func(items []install.SelectItem, _ bool) []string { return idsOf(items) },
		IsOnPath:             func(string) bool { return true },
	})
	if len(calls) != 0 {
		t.Fatalf("got %v", calls)
	}
}

func TestInstall_AbortsWithExitTwoWhenLoginNeverProducesAToken(t *testing.T) {
	r := RunInstall(context.Background(), nil, InstallDeps{
		Harnesses:            []install.Harness{&fakeHarness{id: "h1", name: "H1"}},
		Login:                func(func(string)) error { return nil },
		GetCachedAccessToken: func() string { return "" },
	})
	if r.ExitCode != 2 {
		t.Fatalf("exit %d", r.ExitCode)
	}
	if !strings.Contains(r.UsageError, "sign-in") {
		t.Fatalf("got %q", r.UsageError)
	}
	if len(r.Outcomes) != 0 {
		t.Fatal("no harness may be touched")
	}
}

func TestUninstall_HasNoLoginGateAtAll(t *testing.T) {
	// Removing a local file registration is not an authenticated action —
	// requiring a browser round trip to undo a local edit would strand exactly
	// the person most likely to want to undo it.
	r := RunUninstall(context.Background(), []string{"--yes"}, UninstallDeps{
		Harnesses: []install.Harness{&fakeHarness{id: "h1", name: "H1"}},
	})
	if r.ExitCode != 0 || r.Outcomes[0].Status != "removed" {
		t.Fatalf("got %+v", r)
	}
}

// --- --only / --yes / interactive selection -----------------------------

func TestInstall_OnlyNarrowsToOneHarnessEvenWhenOthersAreAlsoDetected(t *testing.T) {
	home := installSandbox(t)
	// Cursor is detected via ~/.cursor.
	if err := os.MkdirAll(filepath.Join(home, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Windsurf too, via ~/.codeium/windsurf.
	if err := os.MkdirAll(filepath.Join(home, ".codeium", "windsurf"), 0o755); err != nil {
		t.Fatal(err)
	}

	r := RunInstall(context.Background(), []string{"--only", "cursor", "--yes"}, signedIn())
	if r.ExitCode != 0 {
		t.Fatalf("exit %d", r.ExitCode)
	}
	if len(r.Outcomes) != 1 || r.Outcomes[0].DisplayName != "Cursor" {
		t.Fatalf("got %v", statusLines(r))
	}
	// Never even written.
	if _, err := os.Stat(install.WindsurfConfigPath()); err == nil {
		t.Fatal("a harness --only excluded must not be touched")
	}
}

func TestInstall_OnlyAcceptsACommaSeparatedList(t *testing.T) {
	installSandbox(t)
	r := RunInstall(context.Background(), []string{"--only", "cursor,codex", "--yes"}, signedIn())
	if r.ExitCode != 0 {
		t.Fatalf("exit %d", r.ExitCode)
	}
	want := []string{"Cursor: not-detected", "Codex CLI: not-detected"}
	if !reflect.DeepEqual(statusLines(r), want) {
		t.Fatalf("got %v want %v", statusLines(r), want)
	}
}

func TestInstall_InteractiveSelectionDeclineOneAcceptTheOther(t *testing.T) {
	installSandbox(t)
	a := &fakeHarness{id: "a", name: "A"}
	b := &fakeHarness{id: "b", name: "B"}

	var out strings.Builder
	r := RunInstall(context.Background(), nil, InstallDeps{
		Harnesses:            []install.Harness{a, b},
		GetCachedAccessToken: func() string { return "tok" },
		IsOnPath:             func(string) bool { return true },
		// Decline the first, accept the second (bare Enter = accept).
		Stdin:  strings.NewReader("n\n\n"),
		Stdout: &out,
	})

	if r.Outcomes[0].Status != "skipped" {
		t.Fatalf("A should be skipped: %+v", r.Outcomes[0])
	}
	if r.Outcomes[1].Status != "configured" {
		t.Fatalf("B should be configured: %+v", r.Outcomes[1])
	}
	if !strings.Contains(out.String(), "Configure A? [Y/n]") || !strings.Contains(out.String(), "Configure B? [Y/n]") {
		t.Fatalf("both must be prompted, got %q", out.String())
	}
}

func TestInstall_YesConfiguresEveryDetectedHarnessWithoutReadingStdinAtAll(t *testing.T) {
	installSandbox(t)
	read := false
	r := RunInstall(context.Background(), []string{"--yes"}, InstallDeps{
		Harnesses:            []install.Harness{&fakeHarness{id: "a", name: "A"}, &fakeHarness{id: "b", name: "B"}},
		GetCachedAccessToken: func() string { return "tok" },
		IsOnPath:             func(string) bool { return true },
		Stdin:                readerFunc(func([]byte) (int, error) { read = true; return 0, nil }),
	})
	if read {
		t.Fatal("--yes must not read stdin at all — it has to work with none attached")
	}
	for _, o := range r.Outcomes {
		if o.Status != "configured" {
			t.Fatalf("got %+v", o)
		}
	}
}

func TestInstall_DryRunReportsWouldConfigureAndWritesNothing(t *testing.T) {
	home := installSandbox(t)
	if err := os.MkdirAll(filepath.Join(home, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := RunInstall(context.Background(), []string{"--only", "cursor", "--yes", "--dry-run"}, signedIn())
	if r.Outcomes[0].Status != "would-configure" {
		t.Fatalf("got %+v", r.Outcomes[0])
	}
	if _, err := os.Stat(install.CursorConfigPath()); err == nil {
		t.Fatal("a dry run must write nothing")
	}
}

func TestInstall_WarnsWhenLandfallIsNotResolvableOnPathButOnlyAfterARealWrite(t *testing.T) {
	installSandbox(t)
	var logged []string
	deps := func() InstallDeps {
		return InstallDeps{
			Harnesses:            []install.Harness{&fakeHarness{id: "a", name: "A"}},
			GetCachedAccessToken: func() string { return "tok" },
			IsOnPath:             func(string) bool { return false },
			PromptSelection:      func(items []install.SelectItem, _ bool) []string { return idsOf(items) },
			Log:                  func(f string, a ...any) { logged = append(logged, f) },
		}
	}
	RunInstall(context.Background(), []string{"--yes"}, deps())
	if !anyContains(logged, "not resolvable on PATH") {
		t.Fatalf("a harness told to run a command that does not resolve is a silent dead end: %v", logged)
	}

	logged = nil
	RunInstall(context.Background(), []string{"--yes", "--dry-run"}, deps())
	if anyContains(logged, "not resolvable on PATH") {
		t.Fatal("a dry run has registered nothing that could be broken")
	}
}

func TestInstall_AConflictIsAReportNotAFailureAndExitsZero(t *testing.T) {
	r := RunInstall(context.Background(), []string{"--yes"}, InstallDeps{
		Harnesses: []install.Harness{&fakeHarness{id: "a", name: "A", install: func() install.Outcome {
			return install.Outcome{Status: "conflict", Detail: "differs"}
		}}},
		GetCachedAccessToken: func() string { return "tok" },
		IsOnPath:             func(string) bool { return true },
	})
	if r.ExitCode != 0 {
		t.Fatalf("exit %d", r.ExitCode)
	}
	if got := statusLines(r)[0]; got != "A: conflict — differs" {
		t.Fatalf("got %q", got)
	}
}

func TestInstall_AFailedOutcomeExitsOne(t *testing.T) {
	r := RunInstall(context.Background(), []string{"--yes"}, InstallDeps{
		Harnesses: []install.Harness{&fakeHarness{id: "a", name: "A", install: func() install.Outcome {
			return install.Outcome{Status: "failed", Detail: "boom"}
		}}},
		GetCachedAccessToken: func() string { return "tok" },
		IsOnPath:             func(string) bool { return true },
	})
	if r.ExitCode != 1 {
		t.Fatalf("exit %d", r.ExitCode)
	}
}

func TestUninstall_BuildsItsCandidateListFromHasEntryNotDetect(t *testing.T) {
	// A harness deleted from the machine can still have a landfall entry in
	// its config file, and that entry is what this command exists to remove.
	gone := &fakeHarness{id: "a", name: "A", detect: func() bool { return false }, hasEntry: func() bool { return true }}
	r := RunUninstall(context.Background(), []string{"--yes"}, UninstallDeps{Harnesses: []install.Harness{gone}})
	if r.Outcomes[0].Status != "removed" {
		t.Fatalf("got %+v", r.Outcomes[0])
	}
}

func TestUninstall_SkipsAHarnessTheUserDeclines(t *testing.T) {
	var out strings.Builder
	r := RunUninstall(context.Background(), nil, UninstallDeps{
		Harnesses: []install.Harness{&fakeHarness{id: "a", name: "A"}},
		Stdin:     strings.NewReader("n\n"),
		Stdout:    &out,
	})
	if r.Outcomes[0].Status != "skipped" {
		t.Fatalf("got %+v", r.Outcomes[0])
	}
}

// --- flag parsing --------------------------------------------------------

func TestParseInstallFlags_NoOnlyFlagIsNotTheSameAsAnEmptyOnlyList(t *testing.T) {
	if f := parseInstallFlags([]string{"--yes"}); f.only != nil {
		t.Fatalf("no --only means every harness is a candidate, got %v", f.only)
	}
	if f := parseInstallFlags([]string{"--only", ""}); f.only == nil || len(f.only) != 0 {
		t.Fatalf(`--only "" narrows to nothing, got %v`, f.only)
	}
}

func TestParseInstallFlags_TrimsAndDropsEmptyEntries(t *testing.T) {
	f := parseInstallFlags([]string{"--only", " cursor , , codex "})
	if !reflect.DeepEqual(f.only, []string{"cursor", "codex"}) {
		t.Fatalf("got %v", f.only)
	}
}

func TestParseInstallFlags_FlagsAreOrderIndependent(t *testing.T) {
	f := parseInstallFlags([]string{"--dry-run", "--only", "cursor", "--yes"})
	if !f.yes || !f.dryRun || !reflect.DeepEqual(f.only, []string{"cursor"}) {
		t.Fatalf("got %+v", f)
	}
}

// --- the Cobra wiring ----------------------------------------------------

// These three go through the REAL command tree (argv preprocessing, command
// resolution, the RunE wrapper, the report writer) rather than calling a run
// body, so a regression that left `install` shadowed by a placeholder stub —
// or that never registered `hooks` at all, letting it fall through to the
// default `serve` command — fails here rather than in production.

func TestCommandTree_InstallUninstallAndHooksAreRealCommandsNotStubs(t *testing.T) {
	ui := &UI{Out: os.Stderr, Err: os.Stderr}
	root := newRootCommand(ui, "")
	for _, name := range []string{"install", "uninstall", "hooks"} {
		if !hasCommand(root, name) {
			t.Fatalf("%q must be registered, or an unrecognized name falls through to serve", name)
		}
	}
	for _, stub := range placeholderCommands(ui) {
		switch stub.Name() {
		case "install", "uninstall", "hooks":
			t.Fatalf("%q is implemented and must no longer be a placeholder", stub.Name())
		}
	}
}

func TestCommandTree_InstallRunsEndToEndThroughTheRealArgvGrammar(t *testing.T) {
	home := installSandbox(t)
	seedSession(t)
	if err := os.MkdirAll(filepath.Join(home, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out, errOut strings.Builder
	ui := &UI{Out: &out, Err: &errOut}

	code := run(ui, []string{"install", "--only", "cursor", "--yes"}, func(string) string { return "" })
	if code != 0 {
		t.Fatalf("exit %d (stderr %q)", code, errOut.String())
	}
	if got := strings.TrimSpace(out.String()); got != "Cursor: configured ("+install.CursorConfigPath()+")" {
		t.Fatalf("got %q", got)
	}
	if _, err := os.Stat(install.CursorConfigPath()); err != nil {
		t.Fatalf("the file must actually have been written: %v", err)
	}
}

func TestCommandTree_InstallWithNoCachedSessionReachesTheLoginGate(t *testing.T) {
	installSandbox(t)
	// No seeded session. The gate is asserted through RunInstall with an
	// injected no-op Login rather than the real command tree ON PURPOSE: the
	// production default for Login is auth.Login, which opens a browser and
	// polls for five minutes, so a tree-level test with no session would hang
	// the suite rather than fail it.
	r := RunInstall(context.Background(), []string{"--only", "cursor", "--yes"}, InstallDeps{
		Login:                func(func(string)) error { return nil },
		GetCachedAccessToken: func() string { return "" },
	})
	if r.ExitCode != 2 || !strings.Contains(r.UsageError, "sign-in") {
		t.Fatalf("got %+v", r)
	}
}

func TestCommandTree_HooksInstallRunsEndToEndAndReportsOnStdout(t *testing.T) {
	home := installSandbox(t)
	settings := claudeSettings(t, home)
	var out, errOut strings.Builder
	ui := &UI{Out: &out, Err: &errOut}

	// `hooks install` has no login gate, so this goes all the way to a real
	// file write.
	code := run(ui, []string{"hooks", "install", "--only", "claude-code"}, func(string) string { return "" })
	if code != 0 {
		t.Fatalf("exit %d (stderr %q)", code, errOut.String())
	}
	if got := strings.TrimSpace(out.String()); got != "Claude Code: configured ("+settings+")" {
		t.Fatalf("got %q", got)
	}
	if _, err := os.Stat(settings); err != nil {
		t.Fatalf("the file must actually have been written: %v", err)
	}
}

func TestCommandTree_HooksWithNoSubcommandPrintsUsageOnStderrAndExitsTwo(t *testing.T) {
	installSandbox(t)
	var out, errOut strings.Builder
	ui := &UI{Out: &out, Err: &errOut}

	code := run(ui, []string{"hooks"}, func(string) string { return "" })
	if code != 2 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errOut.String(), "usage: landfall hooks <install|uninstall|policy|") {
		t.Fatalf("stderr %q", errOut.String())
	}
	if out.String() != "" {
		t.Fatalf("stdout must stay empty, got %q", out.String())
	}
}

func TestCommandTree_HooksWithAnUnknownSubcommandExitsTwoRatherThanSilentlySucceeding(t *testing.T) {
	installSandbox(t)
	var out, errOut strings.Builder
	ui := &UI{Out: &out, Err: &errOut}
	if code := run(ui, []string{"hooks", "not-an-event"}, func(string) string { return "" }); code != 2 {
		t.Fatalf("exit %d", code)
	}
}

// --- helpers -------------------------------------------------------------

func idsOf(items []install.SelectItem) []string {
	ids := make([]string, 0, len(items))
	for _, i := range items {
		ids = append(ids, i.ID)
	}
	return ids
}

func anyContains(lines []string, needle string) bool {
	for _, l := range lines {
		if strings.Contains(l, needle) {
			return true
		}
	}
	return false
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

// treeUnder lists every path under root, so a test can assert that a command
// created nothing it had no business creating.
func treeUnder(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(root, func(p string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
