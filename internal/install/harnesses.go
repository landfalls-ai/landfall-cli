// harnesses.go — the HarnessDescriptor registry and all six adapters, in the
// fixed order every report prints: Claude Code, Cursor, VS Code, Codex CLI,
// Claude Desktop, Windsurf. A Go port of `src/install/harnesses.mjs` plus
// `src/install/harnesses/*.mjs`.
//
// Four of the six (Cursor, VS Code's fallback path, Claude Desktop, Windsurf)
// were six near-identical files in the Node source differing only in an id, a
// display name, a config path, a key path and a detect() probe. Here they are
// ONE table-driven type (jsonHarness) with those five as data — the difference
// between them was never behaviour. The two that genuinely differ are their
// own types:
//
//	claudeCodeHarness — writes through `claude mcp add-json` (Claude Code
//	                    ships its own user-scope MCP CLI) and additionally
//	                    best-effort installs the landfall Claude Code PLUGIN.
//	codexHarness      — Codex's config is TOML and this CLI ships no TOML
//	                    library on purpose; writes go through `codex mcp add`
//	                    and idempotency is two narrow regexes, never a parse.
package install

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// Outcome is one harness's line in the Installation Report.
type Outcome struct {
	DisplayName string
	Status      string
	Detail      string
	ConfigPath  string
	// PluginStatus is Claude-Code-only and empty everywhere else, so it is
	// purely additive to the report format.
	PluginStatus string
}

// Harness is one MCP registration target.
type Harness interface {
	ID() string
	DisplayName() string
	// Detect reports whether this harness appears to be installed.
	Detect() bool
	// Install registers landfall, or explains why it did not.
	Install() Outcome
	// HasEntry is a NON-MUTATING peek used by `landfall uninstall` to build
	// its candidate list.
	HasEntry() bool
	// Uninstall removes landfall's own entry, or explains why it did not.
	Uninstall() Outcome
}

// Harnesses is the registry, in report order.
func Harnesses() []Harness {
	return []Harness{
		&claudeCodeHarness{},
		cursorHarness(),
		vscodeHarness(),
		&codexHarness{},
		claudeDesktopHarness(),
		windsurfHarness(),
	}
}

// HarnessByID returns the harness with this id, or nil.
func HarnessByID(id string) Harness {
	for _, h := range Harnesses() {
		if h.ID() == id {
			return h
		}
	}
	return nil
}

const mcpConflictDetail = `an existing "landfall" MCP server entry differs from what this installer would write`

// ---------------------------------------------------------------------------
// The four table-driven direct-JSON adapters
// ---------------------------------------------------------------------------

// jsonHarness is every harness whose install and uninstall are both just the
// shared non-destructive JSON merge against a file at a known path.
type jsonHarness struct {
	id             string
	displayName    string
	entry          map[string]any
	keyPath        []string
	conflictDetail string
	configPath     func() string
	detect         func() bool
	// writeVia, when non-nil and it reports handled=true, performs the write
	// instead of the direct JSON merge. Only VS Code uses it (`code --add-mcp`
	// when the `code` CLI is on PATH); uninstall never does, because VS Code
	// documents no CLI remove.
	writeVia func() (handled bool, err error)
}

func (h *jsonHarness) ID() string          { return h.id }
func (h *jsonHarness) DisplayName() string { return h.displayName }
func (h *jsonHarness) Detect() bool        { return h.detect() }

func (h *jsonHarness) Install() Outcome {
	path := h.configPath()
	plan, err := PlanInstall(path, h.keyPath, h.entry)
	if err != nil {
		return Outcome{Status: "failed", Detail: err.Error(), ConfigPath: path}
	}
	switch plan.Action {
	case ActionAlreadyInstalled:
		return Outcome{Status: ActionAlreadyInstalled, ConfigPath: path}
	case ActionConflict:
		return Outcome{Status: ActionConflict, Detail: h.conflictDetail, ConfigPath: path}
	}
	if h.writeVia != nil {
		handled, err := h.writeVia()
		if err != nil {
			return Outcome{Status: "failed", Detail: err.Error(), ConfigPath: path}
		}
		if handled {
			return Outcome{Status: ActionConfigured, ConfigPath: path}
		}
	}
	if _, err := ApplyInstall(path, h.keyPath, h.entry); err != nil {
		return Outcome{Status: "failed", Detail: err.Error(), ConfigPath: path}
	}
	return Outcome{Status: ActionConfigured, ConfigPath: path}
}

func (h *jsonHarness) HasEntry() bool {
	plan, err := PlanUninstall(h.configPath(), h.keyPath, h.entry)
	if err != nil {
		// Unparseable/unreadable — surface it as a candidate rather than hide it.
		return true
	}
	return plan.Action != ActionNotInstalled
}

func (h *jsonHarness) Uninstall() Outcome {
	path := h.configPath()
	result, err := ApplyUninstall(path, h.keyPath, h.entry)
	if err != nil {
		return Outcome{Status: "failed", Detail: err.Error(), ConfigPath: path}
	}
	switch result.Action {
	case ActionRemoved:
		return Outcome{Status: ActionRemoved, ConfigPath: path}
	case ActionNotInstalled:
		return Outcome{Status: ActionNotInstalled, ConfigPath: path}
	default:
		return Outcome{Status: ActionLeftInPlace, ConfigPath: path}
	}
}

// CursorConfigPath is Cursor's global MCP config.
func CursorConfigPath() string { return filepath.Join(HomeDir(), ".cursor", "mcp.json") }

func cursorHarness() *jsonHarness {
	return &jsonHarness{
		id: "cursor", displayName: "Cursor",
		entry: LandfallEntry(), keyPath: []string{"mcpServers", "landfall"},
		conflictDetail: mcpConflictDetail,
		configPath:     CursorConfigPath,
		detect: func() bool {
			for _, c := range AppBundleCandidates("Cursor", "cursor") {
				if PathExists(c) {
					return true
				}
			}
			return PathExists(filepath.Join(HomeDir(), ".cursor"))
		},
	}
}

// VSCodeConfigPath is VS Code's user-profile mcp.json.
func VSCodeConfigPath() string { return filepath.Join(VSCodeUserDir(), "mcp.json") }

func vscodeHarness() *jsonHarness {
	entry := LandfallEntry()
	return &jsonHarness{
		id: "vscode", displayName: "VS Code",
		entry: entry,
		// "servers", NOT "mcpServers" like every other harness here — a real
		// asymmetry in VS Code's schema, not a typo.
		keyPath:        []string{"servers", "landfall"},
		conflictDetail: `an existing "landfall" server entry differs from what this installer would write`,
		configPath:     VSCodeConfigPath,
		detect: func() bool {
			if IsOnPath("code") {
				return true
			}
			for _, c := range AppBundleCandidates("Visual Studio Code", "Microsoft VS Code") {
				if PathExists(c) {
					return true
				}
			}
			return false
		},
		writeVia: func() (bool, error) {
			if !IsOnPath("code") {
				return false, nil
			}
			// `{name, ...ENTRY}` — the flat shape `code --add-mcp` takes.
			payload := map[string]any{"name": "landfall"}
			for k, v := range entry {
				payload[k] = v
			}
			blob, err := json.Marshal(payload)
			if err != nil {
				return false, err
			}
			if err := runCommand("code", "--add-mcp", string(blob)); err != nil {
				return true, err
			}
			return true, nil
		},
	}
}

// ClaudeDesktopConfigPath is Claude Desktop's config, per OS.
//
// macOS/Windows only — there is no official Linux build, and detect() below
// always returns false elsewhere, so the Windows branch doubles as an inert
// fallback path on other platforms.
func ClaudeDesktopConfigPath() string {
	if runtime.GOOS == "darwin" {
		return filepath.Join(HomeDir(), "Library", "Application Support", "Claude", "claude_desktop_config.json")
	}
	return filepath.Join(AppDataDir(), "Claude", "claude_desktop_config.json")
}

func claudeDesktopHarness() *jsonHarness {
	return &jsonHarness{
		id: "claude-desktop", displayName: "Claude Desktop",
		entry: LandfallEntry(), keyPath: []string{"mcpServers", "landfall"},
		conflictDetail: mcpConflictDetail,
		configPath:     ClaudeDesktopConfigPath,
		detect: func() bool {
			if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
				return false
			}
			for _, c := range AppBundleCandidates("Claude", "Claude") {
				if PathExists(c) {
					return true
				}
			}
			return false
		},
	}
}

// WindsurfConfigPath is Windsurf's MCP config. Windsurf does not create this
// file itself — ApplyInstall creates it (and its parents) the first time
// landfall is configured.
func WindsurfConfigPath() string {
	return filepath.Join(HomeDir(), ".codeium", "windsurf", "mcp_config.json")
}

func windsurfHarness() *jsonHarness {
	return &jsonHarness{
		id: "windsurf", displayName: "Windsurf",
		entry: LandfallEntry(), keyPath: []string{"mcpServers", "landfall"},
		conflictDetail: mcpConflictDetail,
		configPath:     WindsurfConfigPath,
		detect: func() bool {
			for _, c := range AppBundleCandidates("Windsurf", "Windsurf") {
				if PathExists(c) {
					return true
				}
			}
			return PathExists(filepath.Join(HomeDir(), ".codeium", "windsurf"))
		},
	}
}

// ---------------------------------------------------------------------------
// Claude Code — bespoke: CLI-delegated write + best-effort plugin install
// ---------------------------------------------------------------------------

// Claude Code's schema requires "type": "stdio" — a superset of the plain
// {command, args} value every other harness uses.
func claudeCodeEntry() map[string]any {
	return map[string]any{"type": "stdio", "command": "landfall", "args": []any{"serve"}}
}

// Claude Code, uniquely among the supported harnesses, also has a native
// PLUGIN system — a marketplace + plugin manifest that ships not just the MCP
// server but the landfall-investigation-dashboard agent. Wiring that up too is
// what makes `landfall install` seamless for a Claude Code user: no separate
// manual `claude plugin marketplace add` step.
const (
	pluginMarketplaceSource = "landfalls-ai/landfall-cli"
	pluginID                = "landfall-edge-bridge@landfall"
)

type claudeCodeHarness struct{}

func (h *claudeCodeHarness) ID() string          { return "claude-code" }
func (h *claudeCodeHarness) DisplayName() string { return "Claude Code" }
func (h *claudeCodeHarness) Detect() bool        { return IsOnPath("claude") }

// ClaudeCodeConfigPath is Claude Code's own internal MCP config. It is READ to
// decide already-installed/conflict/not-installed (an inspection, not a write,
// so it is safe to depend on) but never written by this CLI — writes go
// through `claude mcp add-json`.
func ClaudeCodeConfigPath() string { return filepath.Join(HomeDir(), ".claude.json") }

// installPluginBestEffort registers the marketplace and installs the plugin at
// user scope. It NEVER fails the install: this rides along with the MCP
// registration, which is the part `landfall install`'s contract actually
// promises, and a plugin-install failure (offline, an unreleased branch, an
// older `claude` CLI without `claude plugin`) must not turn a successful MCP
// registration into a reported failure. Both underlying commands are
// idempotent, so re-running `landfall install` re-attempts a previously failed
// plugin step for free.
func installPluginBestEffort() string {
	if err := runCommand("claude", "plugin", "marketplace", "add", pluginMarketplaceSource, "--scope", "user"); err != nil {
		return "skipped"
	}
	if err := runCommand("claude", "plugin", "install", pluginID, "--scope", "user"); err != nil {
		return "skipped"
	}
	return "installed"
}

func (h *claudeCodeHarness) Install() Outcome {
	path := ClaudeCodeConfigPath()
	entry := claudeCodeEntry()
	plan, err := PlanInstall(path, []string{"mcpServers", "landfall"}, entry)
	if err != nil {
		return Outcome{Status: "failed", Detail: err.Error(), ConfigPath: path}
	}
	switch plan.Action {
	case ActionAlreadyInstalled:
		// Still retries the plugin step — it is idempotent on the claude side,
		// so re-attempting a possibly-earlier-failed plugin install is the point.
		return Outcome{Status: ActionAlreadyInstalled, ConfigPath: path, PluginStatus: installPluginBestEffort()}
	case ActionConflict:
		return Outcome{Status: ActionConflict, Detail: mcpConflictDetail, ConfigPath: path}
	}
	blob, err := json.Marshal(entry)
	if err != nil {
		return Outcome{Status: "failed", Detail: err.Error(), ConfigPath: path}
	}
	if err := runCommand("claude", "mcp", "add-json", "landfall", string(blob), "--scope", "user"); err != nil {
		return Outcome{Status: "failed", Detail: err.Error(), ConfigPath: path}
	}
	return Outcome{Status: ActionConfigured, ConfigPath: path, PluginStatus: installPluginBestEffort()}
}

func (h *claudeCodeHarness) HasEntry() bool {
	plan, err := PlanUninstall(ClaudeCodeConfigPath(), []string{"mcpServers", "landfall"}, claudeCodeEntry())
	if err != nil {
		return true
	}
	return plan.Action != ActionNotInstalled
}

func (h *claudeCodeHarness) Uninstall() Outcome {
	path := ClaudeCodeConfigPath()
	plan, err := PlanUninstall(path, []string{"mcpServers", "landfall"}, claudeCodeEntry())
	if err != nil {
		return Outcome{Status: "failed", Detail: err.Error(), ConfigPath: path}
	}
	switch plan.Action {
	case ActionNotInstalled:
		return Outcome{Status: ActionNotInstalled, ConfigPath: path}
	case ActionLeftInPlace:
		return Outcome{Status: ActionLeftInPlace, ConfigPath: path}
	}
	if err := runCommand("claude", "mcp", "remove", "landfall", "--scope", "user"); err != nil {
		return Outcome{Status: "failed", Detail: err.Error(), ConfigPath: path}
	}
	// Best-effort, mirroring installPluginBestEffort: leaving the plugin behind
	// after `landfall uninstall` would strand a dangling MCP-less agent.
	_ = runCommand("claude", "plugin", "uninstall", pluginID, "--scope", "user")
	return Outcome{Status: ActionRemoved, ConfigPath: path}
}

// ---------------------------------------------------------------------------
// Codex CLI — bespoke: TOML config, no TOML parser
// ---------------------------------------------------------------------------

// Codex's config is TOML, and this CLI deliberately adds no TOML library for
// it — Codex ships its own `codex mcp add`/`codex mcp remove` for writes, and
// idempotency/conflict detection here is a narrow, literal match against
// exactly the `[mcp_servers.landfall]` block landfall itself would produce,
// never a general TOML parse. If Codex's own renderer ever formats that block
// differently than expected, the safe failure mode is reporting
// conflict/left-in-place rather than guessing.
var (
	codexHeaderRE    = regexp.MustCompile(`^[ \t]*\[mcp_servers\.landfall\][ \t]*$`)
	codexTableLineRE = regexp.MustCompile(`^[ \t]*\[`)
	codexExpectedRE  = regexp.MustCompile(`(?s)command\s*=\s*"landfall".*args\s*=\s*\[\s*"serve"\s*\]`)
)

// codexLandfallBlock returns the `[mcp_servers.landfall]` block — its header
// line plus every following line up to (not including) the next table header —
// as an exact substring of `text`, so uninstall can delete precisely those
// bytes and nothing else.
//
// DELIBERATE FIX, NOT A TRANSLITERATION. `src/install/harnesses/codex.mjs:24`
// scans for the block with /\[mcp_servers\.landfall\][^[]*/ — "everything up
// to the next literal [". That is a LATENT BUG, verified against the Node
// source: the next literal `[` in a landfall block is the one opening the
// `args = ["serve"]` ARRAY, not a table header, so the matched block truncates
// mid-line to `[mcp_servers.landfall]\ncommand = "landfall"\nargs = `. Its
// EXPECTED_RE then cannot match, and the shipping Node CLI therefore reports
// `Codex CLI: conflict` on a second `landfall install` and `left-in-place` on
// `landfall uninstall` — for a block it wrote itself, leaving an entry its own
// tooling refuses to remove.
//
// A TOML table header is a `[` at the START of a line, and that is what this
// scans for. Still not a TOML parse (this CLI ships no TOML library on
// purpose); it is the same targeted line-region edit the codex hook host's
// flag insertion uses, which is the technique this file already commits to.
func codexLandfallBlock(text string) (string, bool) {
	lines := strings.SplitAfter(text, "\n")
	start := -1
	for i, line := range lines {
		if codexHeaderRE.MatchString(strings.TrimRight(line, "\r\n")) {
			start = i
			break
		}
	}
	if start < 0 {
		return "", false
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if codexTableLineRE.MatchString(lines[i]) {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], ""), true
}

type codexHarness struct{}

func (h *codexHarness) ID() string          { return "codex" }
func (h *codexHarness) DisplayName() string { return "Codex CLI" }
func (h *codexHarness) Detect() bool        { return IsOnPath("codex") }

// CodexConfigPath is Codex's TOML config.
func CodexConfigPath() string { return filepath.Join(HomeDir(), ".codex", "config.toml") }

func (h *codexHarness) Install() Outcome {
	path := CodexConfigPath()
	text, err := readTextOrEmpty(path)
	if err != nil {
		return Outcome{Status: "failed", Detail: err.Error(), ConfigPath: path}
	}
	if block, found := codexLandfallBlock(text); found {
		if codexExpectedRE.MatchString(block) {
			return Outcome{Status: ActionAlreadyInstalled, ConfigPath: path}
		}
		return Outcome{
			Status:     ActionConflict,
			Detail:     "an existing [mcp_servers.landfall] block differs from what this installer would write",
			ConfigPath: path,
		}
	}
	if err := runCommand("codex", "mcp", "add", "landfall", "--", "landfall", "serve"); err != nil {
		return Outcome{Status: "failed", Detail: err.Error(), ConfigPath: path}
	}
	return Outcome{Status: ActionConfigured, ConfigPath: path}
}

func (h *codexHarness) HasEntry() bool {
	text, err := readTextOrEmpty(CodexConfigPath())
	if err != nil {
		return true
	}
	_, found := codexLandfallBlock(text)
	return found
}

func (h *codexHarness) Uninstall() Outcome {
	path := CodexConfigPath()
	text, err := readTextOrEmpty(path)
	if err != nil {
		return Outcome{Status: "failed", Detail: err.Error(), ConfigPath: path}
	}
	block, found := codexLandfallBlock(text)
	if !found {
		return Outcome{Status: ActionNotInstalled, ConfigPath: path}
	}
	if !codexExpectedRE.MatchString(block) {
		return Outcome{Status: ActionLeftInPlace, ConfigPath: path}
	}
	if err := runCommand("codex", "mcp", "remove", "landfall"); err == nil {
		return Outcome{Status: ActionRemoved, ConfigPath: path}
	}
	// `codex mcp remove` is not confirmed to exist — fall back to deleting only
	// the exact block we just matched, byte for byte.
	if err := writeText(path, strings.Replace(text, block, "", 1)); err != nil {
		return Outcome{Status: "failed", Detail: err.Error(), ConfigPath: path}
	}
	return Outcome{Status: ActionRemoved, ConfigPath: path}
}

// ---------------------------------------------------------------------------
// Shared subprocess + text-file helpers
// ---------------------------------------------------------------------------

// execCommand is the seam every subprocess in this package goes through, so a
// test can observe an invocation without a real `claude`/`codex`/`code` binary
// being installed. Production always uses exec.Command.
var execCommand = exec.Command

// runCommand runs an argv ARRAY — never a shell string. Nothing here is ever
// interpolated into a shell, so a config path or an entry value containing a
// quote or a semicolon cannot become an injection.
func runCommand(name string, args ...string) error {
	return execCommand(name, args...).Run()
}

// readTextOrEmpty reads a text (non-JSON) config file, treating a missing file
// as empty — the same ENOENT-is-empty rule ReadJSONOrEmpty applies. Any other
// read error is returned: an unreadable file is not an absent one.
func readTextOrEmpty(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return string(raw), nil
}

// writeText writes a text config file, creating parent directories as needed.
func writeText(path, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(text), 0o644)
}
