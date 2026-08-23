package hosts

// hosts_test.go — the three hook hosts (T050), ported from
// test/hooks/hosts.test.mjs (9 cases) and test/hooks/claude-code-host.test.mjs
// (7 cases).
//
// Per-host config SHAPE is the acceptance criterion here ("passes that host's
// validation"), and each of the three has a different format — so these assert
// on the exact file contents, not just on the reported action.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/landfalls-ai/landfall-cli/internal/install"
)

// --- sandbox -------------------------------------------------------------

type sandbox struct {
	homeDir string
	pathDir string
	appRoot string
}

// newSandbox gives each test a HOME that is not the developer's own and a PATH
// that is REPLACED rather than extended — a `claude` or `cursor` genuinely
// installed on the machine running the suite must not leak into Detect().
func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	root := t.TempDir()
	s := &sandbox{
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
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(s.homeDir, ".config"))
	t.Setenv("PATH", strings.Join([]string{s.pathDir, "/usr/bin", "/bin"}, string(os.PathListSeparator)))
	t.Setenv("LANDFALL_TEST_APP_ROOT", s.appRoot)
	return s
}

func (s *sandbox) mkdir(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(append([]string{s.homeDir}, parts...)...)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

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

func writeText(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func matcherGroup(matcher, command string) map[string]any {
	e := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command}}}
	if matcher != "" {
		e["matcher"] = matcher
	}
	return e
}

// --- registry ------------------------------------------------------------

func TestHosts_ThreeHostsNotSixAndInReportOrder(t *testing.T) {
	var got []string
	for _, h := range Hosts() {
		got = append(got, h.DisplayName())
	}
	// Claude Desktop, VS Code and Windsurf have no lifecycle-hook surface, so
	// they are not hosts at all — never silently reported as "not-detected".
	want := []string{"Claude Code", "Codex CLI", "Cursor"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if HostByID("claude-desktop") != nil || HostByID("vscode") != nil || HostByID("windsurf") != nil {
		t.Fatal("an MCP harness without a hook surface must not resolve as a hook host")
	}
}

// --- Claude Code: shape ---------------------------------------------------

func TestClaudeCode_MatcherGroupShapeEveryEventAndTheStatusLineSurface(t *testing.T) {
	s := newSandbox(t)
	s.mkdir(t, ".claude")
	h := HostByID("claude-code")
	if _, err := h.Install(); err != nil {
		t.Fatal(err)
	}

	want := map[string]any{
		"hooks": map[string]any{
			"Stop": []any{matcherGroup("", "landfall hooks stop")},
			// FileChanged carries a matcher and Stop does not, deliberately.
			// For FileChanged the matcher is REQUIRED, not a refinement: Claude
			// Code watches a list of literal filenames, and an empty or omitted
			// matcher watches nothing and never fires.
			"FileChanged": []any{matcherGroup("room_events", "landfall hooks file-changed")},
			// The delivery half. No matcher — the event does not support one and
			// always fires, which is precisely why it is the half that speaks.
			"UserPromptSubmit": []any{matcherGroup("", "landfall hooks user-prompt-submit")},
			// PreToolUse carries a matcher for a DIFFERENT reason than
			// FileChanged: scoping to the shell tool is what stops an Edit or a
			// Read from spawning a process at all.
			"PreToolUse": []any{matcherGroup("Bash", "landfall hooks pre-tool-use")},
		},
		// A separate config surface in the same file: one key holding one
		// object, not a hooks.<Event> list. Claude Code only.
		"statusLine": map[string]any{"type": "command", "command": "landfall status"},
	}
	got := readJSONFile(t, h.ConfigPath())
	if !install.EqualJSON(got, want) {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
}

func TestClaudeCodeAndCodex_KeepTheBareCommandOnlyAHostNeedingAnotherOutputShapeIsFlagged(t *testing.T) {
	s := newSandbox(t)
	s.mkdir(t, ".claude")
	s.mkdir(t, ".codex")
	claude, codex := HostByID("claude-code"), HostByID("codex")
	if _, err := claude.Install(); err != nil {
		t.Fatal(err)
	}
	if _, err := codex.Install(); err != nil {
		t.Fatal(err)
	}

	commands := []string{
		commandAt(t, readJSONFile(t, claude.ConfigPath()), "hooks", "Stop"),
		commandAt(t, readJSONFile(t, claude.ConfigPath()), "hooks", "FileChanged"),
		commandAt(t, readJSONFile(t, codex.ConfigPath()), "hooks", "Stop"),
	}
	for _, c := range commands {
		if strings.Contains(c, "--host") {
			t.Fatalf("%q must stay byte-identical across the #228 upgrade", c)
		}
	}
}

func commandAt(t *testing.T, data map[string]any, container, event string) string {
	t.Helper()
	list := data[container].(map[string]any)[event].([]any)
	group := list[0].(map[string]any)
	return group["hooks"].([]any)[0].(map[string]any)["command"].(string)
}

// --- Claude Code: the two surfaces compose ------------------------------

func TestClaudeCode_AFreshInstallWritesBothSurfacesAndReportsConfigured(t *testing.T) {
	s := newSandbox(t)
	s.mkdir(t, ".claude")
	h := HostByID("claude-code")

	plan, err := h.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != install.ActionWrite || plan.Hooks.Action != install.ActionWrite || plan.StatusLine.Action != install.ActionWrite {
		t.Fatalf("got %+v", plan)
	}
	if fileExists(h.ConfigPath()) {
		t.Fatal("Plan must not write anything")
	}

	result, err := h.Install()
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != install.ActionConfigured {
		t.Fatalf("got %q", result.Action)
	}
	data := readJSONFile(t, h.ConfigPath())
	if !install.EqualJSON(data["statusLine"], StatusLineEntry()) {
		t.Fatalf("statusLine: %v", data["statusLine"])
	}
	if _, ok := data["hooks"].(map[string]any)["Stop"].([]any); !ok {
		t.Fatal("hooks.Stop must be a list")
	}
}

func TestClaudeCode_ASecondInstallIsIdempotentAcrossBothSurfaces(t *testing.T) {
	s := newSandbox(t)
	s.mkdir(t, ".claude")
	h := HostByID("claude-code")
	if _, err := h.Install(); err != nil {
		t.Fatal(err)
	}
	before := readText(t, h.ConfigPath())

	result, err := h.Install()
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != install.ActionAlreadyInstalled {
		t.Fatalf("got %q", result.Action)
	}
	if readText(t, h.ConfigPath()) != before {
		t.Fatal("neither surface may be rewritten")
	}
	// And not merely "no second write" — no duplicate entry either.
	data := readJSONFile(t, h.ConfigPath())["hooks"].(map[string]any)
	for _, event := range []string{"Stop", "FileChanged", "UserPromptSubmit", "PreToolUse"} {
		if n := len(data[event].([]any)); n != 1 {
			t.Fatalf("%s has %d entries, want 1", event, n)
		}
	}
}

func TestClaudeCode_AHandEditedStatusLineIsAConflictAndBlocksHooksFromInstallingToo(t *testing.T) {
	s := newSandbox(t)
	s.mkdir(t, ".claude")
	h := HostByID("claude-code")
	before := `{
  "statusLine": {
    "type": "command",
    "command": "my-own-status-script"
  }
}`
	writeText(t, h.ConfigPath(), before)

	result, err := h.Install()
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != install.ActionConflict || result.StatusLine.Action != install.ActionConflict {
		t.Fatalf("got %+v", result)
	}
	// Plan-time state — nothing was actually applied.
	if result.Hooks.Action != install.ActionWrite {
		t.Fatalf("hooks plan state: %q", result.Hooks.Action)
	}
	if readText(t, h.ConfigPath()) != before {
		t.Fatal("not one byte written, anywhere — hooks did NOT install either")
	}
}

func TestClaudeCode_AHandEditedHookEntryIsAConflictAndBlocksStatusLineToo(t *testing.T) {
	s := newSandbox(t)
	s.mkdir(t, ".claude")
	h := HostByID("claude-code")
	before := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"landfall hooks stop --my-flag"}]}]}}`
	writeText(t, h.ConfigPath(), before)

	result, err := h.Install()
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != install.ActionConflict || result.Hooks.Action != install.ActionConflict {
		t.Fatalf("got %+v", result)
	}
	if result.StatusLine.Action != install.ActionWrite {
		t.Fatalf("statusLine plan state: %q", result.StatusLine.Action)
	}
	if readText(t, h.ConfigPath()) != before {
		t.Fatal("not one byte written")
	}
}

func TestClaudeCode_UninstallStaysSelectiveUnlikeInstall(t *testing.T) {
	s := newSandbox(t)
	s.mkdir(t, ".claude")
	h := HostByID("claude-code")
	if _, err := h.Install(); err != nil {
		t.Fatal(err)
	}
	// Hand-edit ONLY the Stop entry. The other three registrations stay clean,
	// so the file-level action is still `removed` overall — `left-in-place`
	// applies only when EVERY registration is a conflict. The point under test
	// is narrower and more important than the file-level label: the edited
	// entry itself survives untouched regardless.
	data := readJSONFile(t, h.ConfigPath())
	group := data["hooks"].(map[string]any)["Stop"].([]any)[0].(map[string]any)
	group["hooks"].([]any)[0].(map[string]any)["command"] = "landfall hooks stop --hand-edited"
	blob, _ := json.Marshal(data)
	writeText(t, h.ConfigPath(), string(blob))

	result, err := h.Uninstall()
	if err != nil {
		t.Fatal(err)
	}
	if result.Hooks.Action != install.ActionRemoved {
		t.Fatalf("3 of 4 registrations were clean: got %q", result.Hooks.Action)
	}
	if result.StatusLine.Action != install.ActionRemoved || result.Action != install.ActionRemoved {
		t.Fatalf("got %+v", result)
	}

	after := readJSONFile(t, h.ConfigPath())
	hooksAfter := after["hooks"].(map[string]any)
	if got := commandAt(t, after, "hooks", "Stop"); got != "landfall hooks stop --hand-edited" {
		t.Fatalf("the edited entry itself must survive, got %q", got)
	}
	if _, still := hooksAfter["FileChanged"]; still {
		t.Fatal("the three clean registrations must be removed")
	}
	if _, still := after["statusLine"]; still {
		t.Fatal("the clean statusLine surface must be removed too")
	}
}

func TestClaudeCode_UninstallLeavesAnEmptyFileNotOrphanedEmptyKeys(t *testing.T) {
	s := newSandbox(t)
	s.mkdir(t, ".claude")
	h := HostByID("claude-code")
	if _, err := h.Install(); err != nil {
		t.Fatal(err)
	}
	result, err := h.Uninstall()
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != install.ActionRemoved {
		t.Fatalf("got %q", result.Action)
	}
	if got := readJSONFile(t, h.ConfigPath()); len(got) != 0 {
		t.Fatalf("the empty hooks container is pruned too, got %v", got)
	}
}

func TestClaudeCode_HasEntryIsTrueIfEitherSurfaceIsOurs(t *testing.T) {
	s := newSandbox(t)
	s.mkdir(t, ".claude")
	h := HostByID("claude-code")
	if h.HasEntry() {
		t.Fatal("clean machine")
	}
	if _, err := h.Install(); err != nil {
		t.Fatal(err)
	}
	if !h.HasEntry() {
		t.Fatal("after install")
	}
	if _, err := h.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if h.HasEntry() {
		t.Fatal("after uninstall")
	}

	// And each surface ALONE is enough.
	writeText(t, h.ConfigPath(), `{"statusLine":{"type":"command","command":"landfall status"}}`)
	if !h.HasEntry() {
		t.Fatal("statusLine alone must count")
	}
}

func TestClaudeCode_AUserHookOnTheSameEventSurvivesInstallAndUninstall(t *testing.T) {
	s := newSandbox(t)
	s.mkdir(t, ".claude")
	h := HostByID("claude-code")
	userHook := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "my-own-script.sh"}}}
	blob, _ := json.Marshal(map[string]any{
		"model": "opus",
		"hooks": map[string]any{"Stop": []any{userHook}},
	})
	writeText(t, h.ConfigPath(), string(blob))

	if _, err := h.Install(); err != nil {
		t.Fatal(err)
	}
	installed := readJSONFile(t, h.ConfigPath())
	if installed["model"] != "opus" {
		t.Fatal("unrelated settings preserved")
	}
	stop := installed["hooks"].(map[string]any)["Stop"].([]any)
	if len(stop) != 2 || !install.EqualJSON(stop[0], userHook) {
		t.Fatalf("the user hook must still be FIRST: %v", stop)
	}

	if _, err := h.Uninstall(); err != nil {
		t.Fatal(err)
	}
	after := readJSONFile(t, h.ConfigPath())
	stop = after["hooks"].(map[string]any)["Stop"].([]any)
	if len(stop) != 1 || !install.EqualJSON(stop[0], userHook) {
		t.Fatalf("ours gone, theirs untouched: %v", stop)
	}
	if after["model"] != "opus" {
		t.Fatal("unrelated settings preserved")
	}
	if _, still := after["hooks"].(map[string]any)["FileChanged"]; still {
		t.Fatal("a list we emptied is pruned, not left as []")
	}
}

func TestClaudeCode_AnUnparseableConfigIsReportedNeverOverwritten(t *testing.T) {
	s := newSandbox(t)
	s.mkdir(t, ".claude")
	h := HostByID("claude-code")
	const original = "{ this is not json"
	writeText(t, h.ConfigPath(), original)

	if _, err := h.Install(); err == nil {
		t.Fatal("want an error")
	} else if !strings.Contains(err.Error(), "could not be parsed as JSON") {
		t.Fatalf("want a parse error, got %v", err)
	}
	if readText(t, h.ConfigPath()) != original {
		t.Fatal("never overwritten")
	}
}

// realisticClaudeSettings is a settings.json shaped like one a real Claude
// Code user has: nested objects, arrays, numbers, booleans, an unrecognized
// top-level key, a permissions block, an env block, a pre-existing statusLine
// -adjacent key we do not own, and a hook of the user's own on an event we
// also register. Nothing here except our own two surfaces is any of this
// CLI's business, and all of it must come back untouched.
const realisticClaudeSettings = `{
  "$schema": "https://json.schemastore.org/claude-code-settings.json",
  "model": "opus",
  "cleanupPeriodDays": 30,
  "includeCoAuthoredBy": false,
  "env": {
    "FOO_TOKEN": "abc<def>&ghi",
    "MAX_THINKING_TOKENS": "31999"
  },
  "permissions": {
    "allow": [
      "Bash(git status:*)",
      "Bash(npm run test:*)"
    ],
    "deny": [],
    "additionalDirectories": [
      "/Users/someone/other-repo"
    ]
  },
  "hooks": {
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "~/bin/my-own-stop-hook.sh"
          }
        ]
      }
    ],
    "SessionStart": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "echo hello"
          }
        ]
      }
    ]
  },
  "someKeyThisCLIHasNeverHeardOf": {
    "nested": [
      1,
      2.5,
      true,
      null,
      "x"
    ]
  }
}
`

func TestClaudeCode_RealisticSettingsFileRoundTripsByteIdenticallyAndLosesNothing(t *testing.T) {
	s := newSandbox(t)
	s.mkdir(t, ".claude")
	h := HostByID("claude-code")
	writeText(t, h.ConfigPath(), realisticClaudeSettings)

	if _, err := h.Install(); err != nil {
		t.Fatal(err)
	}

	// Everything the CLI does not own survived the write untouched.
	installed := readJSONFile(t, h.ConfigPath())
	original := map[string]any{}
	if err := json.Unmarshal([]byte(realisticClaudeSettings), &original); err != nil {
		t.Fatal(err)
	}
	for key, want := range original {
		if key == "hooks" {
			continue // the one container we legitimately add into
		}
		if !install.EqualJSON(installed[key], want) {
			t.Fatalf("key %q was disturbed:\n got %v\nwant %v", key, installed[key], want)
		}
	}
	// Including a value with characters encoding/json would HTML-escape by
	// default — a gratuitous edit to a string we only carry through.
	if !strings.Contains(readText(t, h.ConfigPath()), "abc<def>&ghi") {
		t.Fatal("an env value must survive verbatim, unescaped")
	}
	// The user's own hooks, on an event we register and on one we do not.
	hooksAfter := installed["hooks"].(map[string]any)
	if !install.EqualJSON(hooksAfter["SessionStart"], original["hooks"].(map[string]any)["SessionStart"]) {
		t.Fatal("an event we do not register must be left exactly as found")
	}
	stop := hooksAfter["Stop"].([]any)
	if len(stop) != 2 || !install.EqualJSON(stop[0], original["hooks"].(map[string]any)["Stop"].([]any)[0]) {
		t.Fatalf("the user's own Stop hook must stay first and unchanged: %v", stop)
	}

	// And the whole thing round-trips: uninstall leaves the file as found.
	if _, err := h.Uninstall(); err != nil {
		t.Fatal(err)
	}
	after := readJSONFile(t, h.ConfigPath())
	if !install.EqualJSON(after, original) {
		t.Fatalf("install → uninstall did not round-trip:\n got %v\nwant %v", after, original)
	}
	// The file this CLI rewrote must still be 2-space indented with a trailing
	// newline — the same shape the Node CLI leaves behind.
	text := readText(t, h.ConfigPath())
	if !strings.HasSuffix(text, "}\n") || !strings.Contains(text, "\n  \"model\": \"opus\"") {
		t.Fatalf("output formatting drifted:\n%s", text)
	}
}

// --- Cursor ---------------------------------------------------------------

func TestCursor_LowercaseEventNameBareCommandEntryVersionStamped(t *testing.T) {
	s := newSandbox(t)
	s.mkdir(t, ".cursor")
	h := HostByID("cursor")
	if _, err := h.Install(); err != nil {
		t.Fatal(err)
	}
	// `--host cursor` is what tells the handler to answer in JSON on stdout
	// rather than exit 2 + stderr, which Cursor never reads.
	want := map[string]any{
		"version": 1,
		"hooks":   map[string]any{"stop": []any{map[string]any{"command": "landfall hooks stop --host cursor"}}},
	}
	if got := readJSONFile(t, h.ConfigPath()); !install.EqualJSON(got, want) {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
}

func TestCursor_FileChangedIsNotRegisteredItIsAClaudeCodeEvent(t *testing.T) {
	s := newSandbox(t)
	s.mkdir(t, ".cursor")
	h := HostByID("cursor")
	if _, err := h.Install(); err != nil {
		t.Fatal(err)
	}
	events := readJSONFile(t, h.ConfigPath())["hooks"].(map[string]any)
	if len(events) != 1 {
		t.Fatalf("Cursor registers `stop` and nothing else, got %v", events)
	}
	if _, ok := events["stop"]; !ok {
		t.Fatalf("got %v", events)
	}
	// Guard: `beforeSubmitPrompt` was decided against and must not come back.
	if _, wrong := events["beforeSubmitPrompt"]; wrong {
		t.Fatal("beforeSubmitPrompt is deliberately not registered")
	}
}

func TestCursor_AnExistingVersionIsNeverRewritten(t *testing.T) {
	s := newSandbox(t)
	s.mkdir(t, ".cursor")
	h := HostByID("cursor")
	writeText(t, h.ConfigPath(), "{\"version\": 2}\n")
	if _, err := h.Install(); err != nil {
		t.Fatal(err)
	}
	got := readJSONFile(t, h.ConfigPath())["version"]
	if !install.EqualJSON(got, 2) {
		t.Fatalf("a version a future Cursor put there is not ours to correct, got %v", got)
	}
}

func TestCursor_UpgradesTheSupersededBareEntryInPlaceRatherThanStrandingAConflict(t *testing.T) {
	s := newSandbox(t)
	s.mkdir(t, ".cursor")
	h := HostByID("cursor")
	// Exactly what v0.2.0 wrote, before the --host flag existed.
	writeText(t, h.ConfigPath(), `{"version":1,"hooks":{"stop":[{"command":"landfall hooks stop"}]}}`)

	plan, err := h.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != install.ActionWrite {
		t.Fatalf("a stale-but-ours entry is work to do, got %q", plan.Action)
	}
	if _, err := h.Install(); err != nil {
		t.Fatal(err)
	}
	list := readJSONFile(t, h.ConfigPath())["hooks"].(map[string]any)["stop"].([]any)
	if len(list) != 1 {
		t.Fatalf("upgraded in place, not duplicated: %v", list)
	}
	if !install.EqualJSON(list[0], map[string]any{"command": "landfall hooks stop --host cursor"}) {
		t.Fatalf("got %v", list[0])
	}
}

func TestCursor_UninstallRemovesTheSupersededFormToo(t *testing.T) {
	s := newSandbox(t)
	s.mkdir(t, ".cursor")
	h := HostByID("cursor")
	writeText(t, h.ConfigPath(), `{"version":1,"hooks":{"stop":[{"command":"landfall hooks stop"}]}}`)

	result, err := h.Uninstall()
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != install.ActionRemoved {
		t.Fatalf(`"uninstall removed everything landfall added" must include an older version's entry, got %q`, result.Action)
	}
	after := readJSONFile(t, h.ConfigPath())
	if _, still := after["hooks"]; still {
		t.Fatalf("the emptied container is pruned: %v", after)
	}
	if !install.EqualJSON(after["version"], 1) {
		t.Fatal("the version stamp is not a landfall entry and stays")
	}
}

// --- Codex: hooks.json PLUS the TOML feature flag ------------------------

func TestCodex_HooksJSONWrittenAndCodexHooksFlippedOnInConfigTOML(t *testing.T) {
	s := newSandbox(t)
	s.mkdir(t, ".codex")
	toml := CodexTOMLPath()
	writeText(t, toml, "model = \"gpt-5\"\n\n[mcp_servers.other]\ncommand = \"x\"\n")

	h := HostByID("codex")
	result, err := h.Install()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.Detail, "set codex_hooks = true in ") {
		t.Fatalf("detail must say what changed, got %q", result.Detail)
	}

	want := map[string]any{"hooks": map[string]any{
		"Stop":       []any{matcherGroup("", "landfall hooks stop")},
		"PreToolUse": []any{matcherGroup("Bash", "landfall hooks pre-tool-use")},
	}}
	if got := readJSONFile(t, h.ConfigPath()); !install.EqualJSON(got, want) {
		t.Fatalf("got  %v\nwant %v", got, want)
	}

	text := readText(t, toml)
	if !strings.Contains(text, "\ncodex_hooks = true\n") && !strings.HasPrefix(text, "codex_hooks = true\n") {
		t.Fatalf("flag not set as its own line: %q", text)
	}
	if strings.Index(text, "codex_hooks") > strings.Index(text, "[mcp_servers.other]") {
		t.Fatal("a root key must precede the first table or TOML reads it as a member of that table")
	}
	if !strings.Contains(text, `model = "gpt-5"`) {
		t.Fatal("existing config preserved")
	}
}

func TestCodex_UninstallRemovesOurEntryAndDeliberatelyLeavesCodexHooksAlone(t *testing.T) {
	s := newSandbox(t)
	s.mkdir(t, ".codex")
	h := HostByID("codex")
	if _, err := h.Install(); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Uninstall(); err != nil {
		t.Fatal(err)
	}

	if got := readJSONFile(t, h.ConfigPath()); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
	// The flag is a host-wide switch other hooks may depend on — "removes only
	// our entries" is meant literally.
	if !strings.Contains(readText(t, CodexTOMLPath()), "codex_hooks = true") {
		t.Fatal("the feature flag must be left alone")
	}
}

func TestCodex_EntriesAlreadyPresentButTheFlagOffIsStillReportedAsWorkDone(t *testing.T) {
	s := newSandbox(t)
	s.mkdir(t, ".codex")
	h := HostByID("codex")
	if _, err := h.Install(); err != nil {
		t.Fatal(err)
	}
	writeText(t, CodexTOMLPath(), "codex_hooks = false\n")

	result, err := h.Install()
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != install.ActionAlreadyInstalled {
		t.Fatalf("got %q", result.Action)
	}
	if !strings.HasPrefix(result.Detail, "flipped codex_hooks = true") {
		t.Fatalf("a file that had the flag off was NOT installed in any sense that mattered: %q", result.Detail)
	}
	if readText(t, CodexTOMLPath()) != "codex_hooks = true\n" {
		t.Fatalf("got %q", readText(t, CodexTOMLPath()))
	}
}

func TestEnableFlagText_PlacementAndIdempotencyWithoutTouchingADisk(t *testing.T) {
	if _, r := EnableFlagText("codex_hooks = true\n"); r != FlagAlreadyEnabled {
		t.Fatalf("got %q", r)
	}
	if text, _ := EnableFlagText("codex_hooks = false\n"); text != "codex_hooks = true\n" {
		t.Fatalf("got %q", text)
	}
	if text, _ := EnableFlagText(""); strings.TrimSpace(text) != "codex_hooks = true" {
		t.Fatalf("got %q", text)
	}

	// The case that makes this a text edit rather than an append: a file ending
	// inside a table. Appending would define [tbl].codex_hooks, not the root key.
	if text, _ := EnableFlagText("[tbl]\nk = 1\n"); text != "codex_hooks = true\n\n[tbl]\nk = 1\n" {
		t.Fatalf("got %q", text)
	}

	// A same-named key INSIDE a table is a different key and must not be read
	// as the root flag being present — otherwise hooks end up registered but
	// permanently disabled.
	text, result := EnableFlagText("[tbl]\ncodex_hooks = true\n")
	if result != FlagEnabled {
		t.Fatalf("got %q", result)
	}
	if text != "codex_hooks = true\n\n[tbl]\ncodex_hooks = true\n" {
		t.Fatalf("got %q", text)
	}
}

func TestCodex_AConflictBlocksTheWriteAndTheFlagIsNotTouchedEither(t *testing.T) {
	s := newSandbox(t)
	s.mkdir(t, ".codex")
	h := HostByID("codex")
	const before = `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"landfall hooks stop --edited"}]}]}}`
	writeText(t, h.ConfigPath(), before)

	result, err := h.Install()
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != install.ActionConflict {
		t.Fatalf("got %q", result.Action)
	}
	if readText(t, h.ConfigPath()) != before {
		t.Fatal("not one byte written")
	}
	if fileExists(CodexTOMLPath()) {
		t.Fatal("a conflict must not enable the feature flag either")
	}
}
