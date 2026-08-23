package install

// harnesses_test.go — the six MCP-harness adapters (T049), ported from
// test/install/{us1-single-harness,us2-conflict,us2-idempotent,us2-uninstall,
// us2-left-in-place,claude-code-plugin}.test.mjs.
//
// Real files in a sandboxed HOME throughout; the two adapters that shell out
// to their own CLI go through the execCommand seam so nothing here depends on
// `claude`/`codex`/`code` actually being installed on the machine running the
// suite.

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

func TestHarnesses_TheRegistryOrderIsFixedAndIsWhatEveryReportPrints(t *testing.T) {
	var got []string
	for _, h := range Harnesses() {
		got = append(got, h.DisplayName())
	}
	want := []string{"Claude Code", "Cursor", "VS Code", "Codex CLI", "Claude Desktop", "Windsurf"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestHarnessByID_ResolvesEveryRegisteredIDAndNothingElse(t *testing.T) {
	for _, id := range []string{"claude-code", "cursor", "vscode", "codex", "claude-desktop", "windsurf"} {
		if HarnessByID(id) == nil {
			t.Fatalf("%s must resolve", id)
		}
	}
	if HarnessByID("not-a-real-harness") != nil {
		t.Fatal("an unknown id must resolve to nothing")
	}
}

// --- Cursor: the representative table-driven direct-JSON adapter ----------

func TestCursor_InstallWritesMcpServersLandfallAndLeavesOtherSettingsUntouched(t *testing.T) {
	s := newSandbox(t)
	cfg := s.path(".cursor", "mcp.json")
	writeJSONFile(t, cfg, map[string]any{
		"mcpServers":       map[string]any{"other": map[string]any{"command": "foo", "args": []any{"bar"}}},
		"someOtherSetting": true,
	})

	out := HarnessByID("cursor").Install()
	if out.Status != "configured" || out.ConfigPath != cfg {
		t.Fatalf("got %+v", out)
	}
	after := readJSONFile(t, cfg)
	servers := after["mcpServers"].(map[string]any)
	if !EqualJSON(servers["landfall"], map[string]any{"command": "landfall", "args": []any{"serve"}}) {
		t.Fatalf("wrong entry: %v", servers["landfall"])
	}
	if !EqualJSON(servers["other"], map[string]any{"command": "foo", "args": []any{"bar"}}) {
		t.Fatal("a neighbouring server must survive")
	}
	if after["someOtherSetting"] != true {
		t.Fatal("an unrelated setting must survive")
	}
}

func TestCursor_ASecondInstallIsAlreadyInstalledAndTheFileDoesNotChange(t *testing.T) {
	s := newSandbox(t)
	cfg := s.path(".cursor", "mcp.json")
	writeJSONFile(t, cfg, map[string]any{})

	if out := HarnessByID("cursor").Install(); out.Status != "configured" {
		t.Fatalf("first run: %+v", out)
	}
	first := readText(t, cfg)
	if out := HarnessByID("cursor").Install(); out.Status != "already-installed" {
		t.Fatalf("second run: %+v", out)
	}
	if readText(t, cfg) != first {
		t.Fatal("the second run must not rewrite the file")
	}
}

func TestCursor_InstallReportsConflictAndDoesNotOverwriteADifferingEntry(t *testing.T) {
	s := newSandbox(t)
	cfg := s.path(".cursor", "mcp.json")
	writeJSONFile(t, cfg, map[string]any{"mcpServers": map[string]any{
		"landfall": map[string]any{"command": "some-other-thing", "args": []any{"--weird"}},
	}})
	before := readText(t, cfg)

	out := HarnessByID("cursor").Install()
	if out.Status != "conflict" {
		t.Fatalf("got %+v", out)
	}
	if out.Detail == "" {
		t.Fatal("a conflict must explain itself — the report line prints the detail")
	}
	if readText(t, cfg) != before {
		t.Fatal("not one byte may be written")
	}
}

func TestCursor_UninstallRemovesOnlyTheLandfallEntry(t *testing.T) {
	s := newSandbox(t)
	cfg := s.path(".cursor", "mcp.json")
	writeJSONFile(t, cfg, map[string]any{"mcpServers": map[string]any{}})
	HarnessByID("cursor").Install()

	// The user adds their own, unrelated server by hand afterward.
	after := readJSONFile(t, cfg)
	after["mcpServers"].(map[string]any)["someOtherTool"] = map[string]any{"command": "other-tool", "args": []any{}}
	after["unrelatedTopLevelSetting"] = "keep-me"
	writeJSONFile(t, cfg, after)

	out := HarnessByID("cursor").Uninstall()
	if out.Status != "removed" {
		t.Fatalf("got %+v", out)
	}
	final := readJSONFile(t, cfg)
	servers := final["mcpServers"].(map[string]any)
	if _, still := servers["landfall"]; still {
		t.Fatal("the landfall entry must be gone")
	}
	if !EqualJSON(servers["someOtherTool"], map[string]any{"command": "other-tool", "args": []any{}}) {
		t.Fatal("the user's own server must survive")
	}
	if final["unrelatedTopLevelSetting"] != "keep-me" {
		t.Fatal("unrelated settings must survive")
	}
}

func TestCursor_UninstallLeavesAHandEditedEntryInPlace(t *testing.T) {
	s := newSandbox(t)
	cfg := s.path(".cursor", "mcp.json")
	writeJSONFile(t, cfg, map[string]any{"mcpServers": map[string]any{}})
	HarnessByID("cursor").Install()

	edited := readJSONFile(t, cfg)
	handEdited := map[string]any{"command": "landfall", "args": []any{"serve", "--verbose"}}
	edited["mcpServers"].(map[string]any)["landfall"] = handEdited
	writeJSONFile(t, cfg, edited)

	if out := HarnessByID("cursor").Uninstall(); out.Status != "left-in-place" {
		t.Fatalf("got %+v", out)
	}
	got := readJSONFile(t, cfg)["mcpServers"].(map[string]any)["landfall"]
	if !EqualJSON(got, handEdited) {
		t.Fatalf("the hand-edit must survive, got %v", got)
	}
}

func TestCursor_HasEntryIsTrueOnlyWhenSomethingOfOursIsThere(t *testing.T) {
	s := newSandbox(t)
	h := HarnessByID("cursor")
	if h.HasEntry() {
		t.Fatal("a clean machine has no entry")
	}
	h.Install()
	if !h.HasEntry() {
		t.Fatal("after install there is one")
	}
	h.Uninstall()
	if h.HasEntry() {
		t.Fatal("after uninstall there is not")
	}
	_ = s
}

func TestCursor_DetectFindsTheDotCursorDirectory(t *testing.T) {
	s := newSandbox(t)
	h := HarnessByID("cursor")
	if h.Detect() {
		t.Fatal("nothing is installed in a fresh sandbox")
	}
	writeJSONFile(t, s.path(".cursor", "mcp.json"), map[string]any{})
	if !h.Detect() {
		t.Fatal("a ~/.cursor directory means Cursor")
	}
}

// --- VS Code: the `servers` key asymmetry and the `code --add-mcp` path ---

func TestVSCode_UsesTheServersKeyNotMcpServers(t *testing.T) {
	newSandbox(t)
	// No `code` on PATH, so this takes the direct-merge fallback.
	out := HarnessByID("vscode").Install()
	if out.Status != "configured" {
		t.Fatalf("got %+v", out)
	}
	after := readJSONFile(t, VSCodeConfigPath())
	if _, wrong := after["mcpServers"]; wrong {
		t.Fatal("VS Code uses `servers`, not `mcpServers` — a real asymmetry, not a typo")
	}
	if !EqualJSON(after["servers"].(map[string]any)["landfall"], map[string]any{"command": "landfall", "args": []any{"serve"}}) {
		t.Fatalf("got %v", after["servers"])
	}
}

func TestVSCode_PrefersTheCodeCLIWhenItIsOnPath(t *testing.T) {
	s := newSandbox(t)
	s.stubOnPath(t, "code")
	rec := &recorder{}
	rec.hook(t)

	if out := HarnessByID("vscode").Install(); out.Status != "configured" {
		t.Fatalf("got %+v", out)
	}
	calls := rec.withPrefix("code", "--add-mcp")
	if len(calls) != 1 {
		t.Fatalf("want exactly one `code --add-mcp`, got %v", rec.calls)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(calls[0][2]), &payload); err != nil {
		t.Fatal(err)
	}
	if !EqualJSON(payload, map[string]any{"name": "landfall", "command": "landfall", "args": []any{"serve"}}) {
		t.Fatalf("wrong --add-mcp payload: %v", payload)
	}
	// The CLI owns the write, so the adapter must not also write the file.
	if fileExists(VSCodeConfigPath()) {
		t.Fatal("the direct merge must not run when the CLI handled the write")
	}
}

func TestVSCode_UninstallAlwaysEditsTheFileSinceThereIsNoDocumentedCLIRemove(t *testing.T) {
	s := newSandbox(t)
	s.stubOnPath(t, "code")
	writeJSONFile(t, VSCodeConfigPath(), map[string]any{"servers": map[string]any{
		"landfall": map[string]any{"command": "landfall", "args": []any{"serve"}},
		"keep":     map[string]any{"command": "keep"},
	}})
	rec := &recorder{}
	rec.hook(t)

	if out := HarnessByID("vscode").Uninstall(); out.Status != "removed" {
		t.Fatalf("got %+v", out)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("uninstall must not shell out, got %v", rec.calls)
	}
	servers := readJSONFile(t, VSCodeConfigPath())["servers"].(map[string]any)
	if _, still := servers["landfall"]; still {
		t.Fatal("ours must be gone")
	}
	if _, gone := servers["keep"]; !gone {
		t.Fatal("theirs must survive")
	}
}

// --- Claude Code: CLI-delegated write + best-effort plugin ---------------

func claudeCodeMCPArgv() []string {
	blob, _ := json.Marshal(map[string]any{"type": "stdio", "command": "landfall", "args": []any{"serve"}})
	return []string{"claude", "mcp", "add-json", "landfall", string(blob), "--scope", "user"}
}

func TestClaudeCode_RegistersTheMCPServerAndThePluginAndReportsBoth(t *testing.T) {
	s := newSandbox(t)
	s.stubOnPath(t, "claude")
	rec := &recorder{}
	rec.hook(t)

	out := HarnessByID("claude-code").Install()
	if out.Status != "configured" || out.PluginStatus != "installed" {
		t.Fatalf("got %+v", out)
	}
	if out.ConfigPath != s.path(".claude.json") {
		t.Fatalf("wrong config path %q", out.ConfigPath)
	}
	want := [][]string{
		claudeCodeMCPArgv(),
		{"claude", "plugin", "marketplace", "add", "landfalls-ai/landfall-cli", "--scope", "user"},
		{"claude", "plugin", "install", "landfall-edge-bridge@landfall", "--scope", "user"},
	}
	if !reflect.DeepEqual(rec.calls, want) {
		t.Fatalf("got  %v\nwant %v", rec.calls, want)
	}
}

func TestClaudeCode_AFailingPluginStepDoesNotFailTheMCPRegistration(t *testing.T) {
	s := newSandbox(t)
	s.stubOnPath(t, "claude")
	rec := &recorder{failIf: func(argv []string) bool { return len(argv) > 1 && argv[1] == "plugin" }}
	rec.hook(t)

	out := HarnessByID("claude-code").Install()
	if out.Status != "configured" {
		t.Fatalf("the MCP registration succeeded and must be reported as such: %+v", out)
	}
	if out.PluginStatus != "skipped" {
		t.Fatalf("want plugin: skipped, got %q", out.PluginStatus)
	}
}

func TestClaudeCode_AlreadyInstalledStillRetriesThePluginStep(t *testing.T) {
	s := newSandbox(t)
	s.stubOnPath(t, "claude")
	// The real `claude mcp add-json` writes this shape; the recorder only
	// records, so seed it directly to simulate a prior successful install.
	writeJSONFile(t, s.path(".claude.json"), map[string]any{"mcpServers": map[string]any{
		"landfall": map[string]any{"type": "stdio", "command": "landfall", "args": []any{"serve"}},
	}})
	rec := &recorder{}
	rec.hook(t)

	out := HarnessByID("claude-code").Install()
	if out.Status != "already-installed" || out.PluginStatus != "installed" {
		t.Fatalf("got %+v", out)
	}
	if n := len(rec.withPrefix("claude", "mcp")); n != 0 {
		t.Fatalf("nothing to register, so no `mcp` call: got %d", n)
	}
	// The plugin step is idempotent on the claude side, so re-attempting a
	// possibly-earlier-failed install is exactly the point.
	if n := len(rec.withPrefix("claude", "plugin")); n != 2 {
		t.Fatalf("want 2 plugin calls, got %d", n)
	}
}

func TestClaudeCode_UninstallRemovesTheServerAndBestEffortThePlugin(t *testing.T) {
	s := newSandbox(t)
	writeJSONFile(t, s.path(".claude.json"), map[string]any{"mcpServers": map[string]any{
		"landfall": map[string]any{"type": "stdio", "command": "landfall", "args": []any{"serve"}},
	}})
	rec := &recorder{failIf: func(argv []string) bool { return len(argv) > 1 && argv[1] == "plugin" }}
	rec.hook(t)

	out := HarnessByID("claude-code").Uninstall()
	if out.Status != "removed" {
		t.Fatalf("a failing plugin uninstall must not fail the removal: %+v", out)
	}
	if len(rec.withPrefix("claude", "mcp", "remove")) != 1 {
		t.Fatalf("got %v", rec.calls)
	}
}

func TestClaudeCode_AHandEditedEntryIsLeftInPlaceAndTheCLIIsNeverCalled(t *testing.T) {
	s := newSandbox(t)
	writeJSONFile(t, s.path(".claude.json"), map[string]any{"mcpServers": map[string]any{
		"landfall": map[string]any{"type": "stdio", "command": "landfall", "args": []any{"serve", "--verbose"}},
	}})
	rec := &recorder{}
	rec.hook(t)

	if out := HarnessByID("claude-code").Uninstall(); out.Status != "left-in-place" {
		t.Fatalf("got %+v", out)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("nothing of ours to remove means nothing to run: %v", rec.calls)
	}
}

// --- Codex: TOML-adjacent idempotency without a TOML parser --------------

func TestCodex_InstallDelegatesToTheCodexCLIOnACleanMachine(t *testing.T) {
	newSandbox(t)
	rec := &recorder{}
	rec.hook(t)

	if out := HarnessByID("codex").Install(); out.Status != "configured" {
		t.Fatalf("got %+v", out)
	}
	want := [][]string{{"codex", "mcp", "add", "landfall", "--", "landfall", "serve"}}
	if !reflect.DeepEqual(rec.calls, want) {
		t.Fatalf("got %v want %v", rec.calls, want)
	}
}

func TestCodex_RecognizesItsOwnBlockAsAlreadyInstalled(t *testing.T) {
	s := newSandbox(t)
	toml := filepath.Join(s.homeDir, ".codex", "config.toml")
	if err := writeText(toml, "model = \"gpt-5\"\n\n[mcp_servers.landfall]\ncommand = \"landfall\"\nargs = [\"serve\"]\n"); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	rec.hook(t)

	if out := HarnessByID("codex").Install(); out.Status != "already-installed" {
		t.Fatalf("got %+v", out)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("nothing to do means nothing to run: %v", rec.calls)
	}
}

func TestCodex_ADifferingBlockIsAConflictAndIsNeverRewritten(t *testing.T) {
	s := newSandbox(t)
	toml := filepath.Join(s.homeDir, ".codex", "config.toml")
	const original = "[mcp_servers.landfall]\ncommand = \"landfall\"\nargs = [\"serve\", \"--verbose\"]\n"
	if err := writeText(toml, original); err != nil {
		t.Fatal(err)
	}

	out := HarnessByID("codex").Install()
	if out.Status != "conflict" {
		t.Fatalf("got %+v", out)
	}
	if readText(t, toml) != original {
		t.Fatal("not one byte may be written")
	}
}

func TestCodex_UninstallFallsBackToDeletingExactlyTheMatchedBlockWhenTheCLIFails(t *testing.T) {
	s := newSandbox(t)
	toml := filepath.Join(s.homeDir, ".codex", "config.toml")
	if err := writeText(toml, "model = \"gpt-5\"\n\n[mcp_servers.landfall]\ncommand = \"landfall\"\nargs = [\"serve\"]\n\n"); err != nil {
		t.Fatal(err)
	}
	// `codex mcp remove` is not confirmed to exist — simulate it not working.
	rec := &recorder{failIf: func([]string) bool { return true }}
	rec.hook(t)

	if out := HarnessByID("codex").Uninstall(); out.Status != "removed" {
		t.Fatalf("got %+v", out)
	}
	after := readText(t, toml)
	if contains(after, "mcp_servers.landfall") {
		t.Fatalf("our block must be gone: %q", after)
	}
	if !contains(after, `model = "gpt-5"`) {
		t.Fatalf("the rest of the file must survive verbatim: %q", after)
	}
}

func TestCodex_UninstallLeavesADifferingBlockInPlace(t *testing.T) {
	s := newSandbox(t)
	toml := filepath.Join(s.homeDir, ".codex", "config.toml")
	const original = "[mcp_servers.landfall]\ncommand = \"something-else\"\n"
	if err := writeText(toml, original); err != nil {
		t.Fatal(err)
	}
	if out := HarnessByID("codex").Uninstall(); out.Status != "left-in-place" {
		t.Fatalf("got %+v", out)
	}
	if readText(t, toml) != original {
		t.Fatal("a block we do not recognize is not ours to delete")
	}
}

func TestCodex_NotInstalledOnACleanMachine(t *testing.T) {
	newSandbox(t)
	h := HarnessByID("codex")
	if h.HasEntry() {
		t.Fatal("nothing is installed")
	}
	if out := h.Uninstall(); out.Status != "not-installed" {
		t.Fatalf("got %+v", out)
	}
}

// --- Windsurf / Claude Desktop paths -------------------------------------

func TestWindsurf_CreatesTheConfigFileAndItsParentsOnFirstInstall(t *testing.T) {
	newSandbox(t)
	if fileExists(WindsurfConfigPath()) {
		t.Fatal("Windsurf does not create this file itself")
	}
	if out := HarnessByID("windsurf").Install(); out.Status != "configured" {
		t.Fatalf("got %+v", out)
	}
	after := readJSONFile(t, WindsurfConfigPath())
	if !EqualJSON(after["mcpServers"].(map[string]any)["landfall"], map[string]any{"command": "landfall", "args": []any{"serve"}}) {
		t.Fatalf("got %v", after)
	}
}

func TestClaudeDesktop_DetectionIsGatedToMacOSAndWindows(t *testing.T) {
	newSandbox(t)
	h := HarnessByID("claude-desktop")
	// A fresh sandbox has no app bundle, so this is false on every platform;
	// the point under test is that it does not panic or probe HOME instead.
	if h.Detect() {
		t.Fatal("nothing is installed in a fresh sandbox")
	}
}

func TestFailedIsTheOnlyStatusThatEverExitsNonZero(t *testing.T) {
	// A conflict is a REPORT, not a failure — contracts/cli.md, and the reason
	// `landfall install` on a machine with a hand-edited entry still exits 0.
	for _, status := range []string{"not-detected", "configured", "already-installed", "skipped", "conflict"} {
		if got := ExitCodeForOutcomes([]Outcome{{Status: status}}); got != 0 {
			t.Fatalf("%s must exit 0, got %d", status, got)
		}
	}
	if got := ExitCodeForOutcomes([]Outcome{{Status: "conflict"}, {Status: "failed"}}); got != 1 {
		t.Fatalf("failed must exit 1, got %d", got)
	}
}
