// Package hosts implements the three lifecycle-hook HOSTS `landfall hooks
// install` writes to, in the order every report prints them: Claude Code,
// Codex CLI, Cursor. A Go port of `src/hooks/hosts.mjs` and
// `src/hooks/hosts/{claude-code,codex,cursor}.mjs`.
//
// Deliberately a SHORTER list than internal/install's six MCP harnesses: MCP
// registration works for any harness that speaks MCP, while a hook needs the
// host to actually have a lifecycle-hook surface. Claude Desktop, VS Code and
// Windsurf have none, so they are not silently reported as "not-detected"
// here — they are simply not hosts.
//
// This is its own package rather than more files in internal/hooks because
// installing a hook is a config-file merge (internal/install's two merge
// cores) while internal/hooks is the RUNTIME that answers a hook invocation.
// Keeping them apart also keeps internal/hooks free of any dependency on the
// installer.
package hosts

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/landfalls-ai/landfall-cli/internal/hooks"
	"github.com/landfalls-ai/landfall-cli/internal/install"
)

// Result is one host's install/uninstall outcome.
type Result struct {
	// Action is one of internal/install's Action* constants.
	Action string
	// Detail is an optional human-readable note (Codex's TOML flag uses it).
	Detail string
	// Hooks is the hooks.* list-merge sub-result. Always set.
	Hooks install.HookPlan
	// StatusLine is the single-key sub-result — Claude Code only, since it is
	// the only host with a statusLine surface.
	StatusLine *install.Plan
	// Flag is the codex_hooks TOML-flag outcome — Codex only, and set by Plan
	// as well as Install. Deliberately NOT folded into Detail on a plan:
	// `src/hooks/hosts/codex.mjs:123-127`'s plan() returns it under its own
	// key, so a `--dry-run` report line carries no detail today. Keeping that
	// split means the dry run reports exactly what the real run reports minus
	// what it did, rather than a note about work it has not done.
	Flag string
}

// Host is one lifecycle-hook host.
type Host interface {
	ID() string
	DisplayName() string
	// ConfigPath is the file the hook entries live in. Reported on every
	// outcome line, including a failure, so the user is told WHICH file.
	ConfigPath() string
	Detect() bool
	// Plan reports what an install would do without writing anything.
	Plan() (Result, error)
	Install() (Result, error)
	// HasEntry is a NON-MUTATING peek used to build `hooks uninstall`'s
	// candidate list. An unreadable/unparseable file answers true — surfacing
	// it beats hiding it.
	HasEntry() bool
	Uninstall() (Result, error)
}

// Hosts is the registry, in report order.
func Hosts() []Host {
	return []Host{&claudeCodeHost{}, &codexHost{}, &cursorHost{}}
}

// HostByID returns the host with this id, or nil.
func HostByID(id string) Host {
	for _, h := range Hosts() {
		if h.ID() == id {
			return h
		}
	}
	return nil
}

// matcherGroupRegistrations builds the Claude-Code-shaped registration for
// each event a host registers: a per-event list of matcher GROUPS, each group
// holding a list of {type: "command", command} hooks.
//
// `matcher` is omitted where the event supports none (Stop, UserPromptSubmit);
// carried for FileChanged, where it is not a refinement but the whole watch
// list (an absent matcher there watches nothing at all); and carried for
// PreToolUse, where narrowing to the shell tool is what keeps a non-matching
// command free.
func matcherGroupRegistrations(hostID string, eventKey map[string]string, prefix string) []install.Registration {
	var regs []install.Registration
	for _, eventID := range hooks.EventsForHost(hostID) {
		key, ok := eventKey[eventID]
		if !ok {
			continue
		}
		entry := map[string]any{
			"hooks": []any{map[string]any{"type": "command", "command": hooks.HookCommand(eventID, hostID)}},
		}
		if m := hooks.MatcherFor(eventID); m != "" {
			entry["matcher"] = m
		}
		regs = append(regs, install.Registration{KeyPath: []string{prefix, key}, Entry: entry})
	}
	return regs
}

// matcherGroupIsOurs recognizes a landfall-authored matcher-group element,
// edited or not: any element whose `hooks` list holds a command starting
// `landfall hooks`.
func matcherGroupIsOurs(element any) bool {
	m, ok := element.(map[string]any)
	if !ok {
		return false
	}
	list, ok := m["hooks"].([]any)
	if !ok {
		return false
	}
	for _, h := range list {
		hm, ok := h.(map[string]any)
		if !ok {
			continue
		}
		cmd, _ := hm["command"].(string)
		if hooks.IsLandfallCommand(cmd) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Claude Code — TWO independent config surfaces in one settings.json
// ---------------------------------------------------------------------------

// StatusLineKey / StatusLineEntry are `landfall status`'s statusLine
// registration. A SEPARATE config surface from the hooks.* lists: Claude
// Code's statusLine key holds ONE object, not a per-event list, so it goes
// through internal/install's single-key merge (the same primitive every
// direct-JSON MCP-registration adapter uses), not the list machinery.
//
// Both surfaces live in the same settings.json but are otherwise independent:
// a customer can reasonably want hooks without a statusline or vice versa, so
// neither blocks the other, and each is its own non-destructive
// read-modify-write — sequential, not simultaneous, so the second call always
// sees what the first one wrote.
const StatusLineKey = "statusLine"

// StatusLineEntry is returned fresh per call rather than shared as a package
// var, for the same reason install.LandfallEntry() is.
func StatusLineEntry() map[string]any {
	return map[string]any{"type": "command", "command": "landfall status"}
}

// Worst-case-wins precedence across two INDEPENDENT surfaces (hooks,
// statusLine), combined into the one action the per-host report expects. Same
// precedence the list merge already uses internally across its own multiple
// registrations, generalized by one level.
var (
	installOrder   = []string{install.ActionAlreadyInstalled, install.ActionWrite, install.ActionConfigured, install.ActionConflict}
	uninstallOrder = []string{install.ActionNotInstalled, install.ActionLeftInPlace, install.ActionRemove, install.ActionRemoved}
)

func combine(order []string, actions ...string) string {
	worst := ""
	worstAt := -1
	for _, a := range actions {
		at := indexOf(order, a)
		if worst == "" || at > worstAt {
			worst, worstAt = a, at
		}
	}
	return worst
}

func indexOf(list []string, want string) int {
	for i, v := range list {
		if v == want {
			return i
		}
	}
	return -1
}

// Claude Code's hook shape: each event holds a list of matcher groups.
var claudeCodeEventKey = map[string]string{
	"stop":               "Stop",
	"file-changed":       "FileChanged",
	"user-prompt-submit": "UserPromptSubmit",
	"pre-tool-use":       "PreToolUse",
}

type claudeCodeHost struct{}

func (h *claudeCodeHost) ID() string          { return "claude-code" }
func (h *claudeCodeHost) DisplayName() string { return "Claude Code" }

func (h *claudeCodeHost) ConfigPath() string {
	return filepath.Join(install.HomeDir(), ".claude", "settings.json")
}

func (h *claudeCodeHost) Detect() bool {
	return install.IsOnPath("claude") || install.PathExists(filepath.Join(install.HomeDir(), ".claude"))
}

func (h *claudeCodeHost) registrations() []install.Registration {
	return matcherGroupRegistrations(h.ID(), claudeCodeEventKey, "hooks")
}

func (h *claudeCodeHost) Plan() (Result, error) {
	hookPlan, err := install.PlanHookInstall(h.ConfigPath(), h.registrations(), matcherGroupIsOurs)
	if err != nil {
		return Result{}, err
	}
	statusLine, err := install.PlanInstall(h.ConfigPath(), []string{StatusLineKey}, StatusLineEntry())
	if err != nil {
		return Result{}, err
	}
	return Result{
		Action:     combine(installOrder, hookPlan.Action, statusLine.Action),
		Hooks:      hookPlan,
		StatusLine: &statusLine,
	}, nil
}

// Install is ATOMIC across both surfaces, on purpose — matching the guarantee
// the hooks.* list merge already gives on its own ("not one byte written" on a
// conflict).
//
// Install and uninstall are NOT symmetric here, and that is deliberate:
// ApplyHookUninstall already removes what it safely can and leaves what is
// hand-edited (selective, per registration), but install has always been
// all-or-nothing — a conflict is a report, not a partial write. Extending
// "one surface's conflict must not block the other's write" to install would
// be a NEW, WEAKER guarantee than hooks.* alone already gives today. So: check
// the combined plan first, and touch the file at all only when nothing
// conflicts.
func (h *claudeCodeHost) Install() (Result, error) {
	combined, err := h.Plan()
	if err != nil {
		return Result{}, err
	}
	if combined.Action == install.ActionConflict {
		return combined, nil
	}
	hookResult, err := install.ApplyHookInstall(h.ConfigPath(), h.registrations(), matcherGroupIsOurs)
	if err != nil {
		return Result{}, err
	}
	statusLine, err := install.ApplyInstall(h.ConfigPath(), []string{StatusLineKey}, StatusLineEntry())
	if err != nil {
		return Result{}, err
	}
	return Result{
		Action:     combine(installOrder, hookResult.Action, statusLine.Action),
		Hooks:      hookResult,
		StatusLine: &statusLine,
	}, nil
}

func (h *claudeCodeHost) HasEntry() bool {
	hookPlan, err := install.PlanHookUninstall(h.ConfigPath(), h.registrations(), matcherGroupIsOurs)
	if err != nil {
		return true
	}
	statusLine, err := install.PlanUninstall(h.ConfigPath(), []string{StatusLineKey}, StatusLineEntry())
	if err != nil {
		return true
	}
	return hookPlan.Action != install.ActionNotInstalled || statusLine.Action != install.ActionNotInstalled
}

func (h *claudeCodeHost) Uninstall() (Result, error) {
	hookResult, err := install.ApplyHookUninstall(h.ConfigPath(), h.registrations(), matcherGroupIsOurs)
	if err != nil {
		return Result{}, err
	}
	statusLine, err := install.ApplyUninstall(h.ConfigPath(), []string{StatusLineKey}, StatusLineEntry())
	if err != nil {
		return Result{}, err
	}
	return Result{
		Action:     combine(uninstallOrder, hookResult.Action, statusLine.Action),
		Hooks:      hookResult,
		StatusLine: &statusLine,
	}, nil
}

// ---------------------------------------------------------------------------
// Codex CLI — hooks.json PLUS a TOML feature flag, without a TOML parser
// ---------------------------------------------------------------------------

// Two files, because Codex gates hooks behind a feature flag:
//
//	~/.codex/hooks.json   the hook entries themselves
//	~/.codex/config.toml  `codex_hooks = true`, without which none of them run
//
// Registering entries into a file the host is configured to ignore is the
// silent-dead-end failure this whole command exists to avoid, so install does
// both or reports what it could not do.
//
// hooks.json is written in Claude Code's shape because Codex's hook surface is
// modeled on Claude Code's. That nesting is the one thing here taken on the
// story's word rather than verified against Codex itself. It is safe to be
// wrong about: every write is an append into a list, so a mismatch leaves an
// inert key rather than breaking a config, and correcting it is a one-line
// change to the key prefix.
//
// The TOML flag is handled by TARGETED TEXT EDIT, not by parsing: this CLI
// ships no TOML library on purpose, and the alternative — writing a parser —
// is a far larger risk to a user's config than inserting one root key.
var codexEventKey = map[string]string{"stop": "Stop", "pre-tool-use": "PreToolUse"}

var (
	codexFlagRE  = regexp.MustCompile(`^[ \t]*codex_hooks[ \t]*=[ \t]*(true|false)[ \t]*$`)
	codexTableRE = regexp.MustCompile(`^[ \t]*\[`)
)

const codexFlagLine = "codex_hooks = true"

// Flag-edit results.
const (
	FlagAlreadyEnabled = "already-enabled"
	FlagEnabled        = "enabled"
	FlagFlipped        = "flipped"
)

type codexHost struct{}

func (h *codexHost) ID() string          { return "codex" }
func (h *codexHost) DisplayName() string { return "Codex CLI" }

func (h *codexHost) ConfigPath() string {
	return filepath.Join(install.HomeDir(), ".codex", "hooks.json")
}

// CodexTOMLPath is Codex's main config, which carries the feature flag.
func CodexTOMLPath() string { return filepath.Join(install.HomeDir(), ".codex", "config.toml") }

func (h *codexHost) Detect() bool {
	return install.IsOnPath("codex") || install.PathExists(filepath.Join(install.HomeDir(), ".codex"))
}

func (h *codexHost) registrations() []install.Registration {
	return matcherGroupRegistrations(h.ID(), codexEventKey, "hooks")
}

func readTOMLText() (string, error) {
	raw, err := os.ReadFile(CodexTOMLPath())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return string(raw), nil
}

// EnableFlagText inserts or flips `codex_hooks = true` as a ROOT key.
//
// A root key must appear BEFORE the first `[table]` header or TOML would read
// it as a member of that table, so this inserts at the top rather than
// appending — appending to a file that ends inside `[some.table]` would
// silently write the wrong key.
//
// Only the ROOT region counts when looking for an existing flag. A
// `codex_hooks` under `[some.table]` is a different key entirely, and treating
// it as this flag would leave hooks registered but permanently disabled — the
// exact silent dead end this function exists to prevent.
//
// Returns the new text and one of FlagAlreadyEnabled / FlagEnabled /
// FlagFlipped.
func EnableFlagText(text string) (string, string) {
	var lines []string
	if len(text) > 0 {
		lines = strings.Split(text, "\n")
	}

	rootEnd := len(lines)
	for i, line := range lines {
		if codexTableRE.MatchString(line) {
			rootEnd = i
			break
		}
	}

	for i := 0; i < rootEnd; i++ {
		if !codexFlagRE.MatchString(lines[i]) {
			continue
		}
		if strings.Contains(lines[i], "true") {
			return text, FlagAlreadyEnabled
		}
		next := append([]string{}, lines...)
		next[i] = codexFlagLine
		return strings.Join(next, "\n"), FlagFlipped
	}

	next := make([]string, 0, len(lines)+2)
	next = append(next, lines[:rootEnd]...)
	next = append(next, codexFlagLine, "")
	next = append(next, lines[rootEnd:]...)
	return strings.Join(next, "\n"), FlagEnabled
}

func ensureCodexFlag() (string, error) {
	text, err := readTOMLText()
	if err != nil {
		return "", err
	}
	next, result := EnableFlagText(text)
	if result != FlagAlreadyEnabled {
		path := CodexTOMLPath()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, []byte(next), 0o644); err != nil {
			return "", err
		}
	}
	return result, nil
}

func (h *codexHost) Plan() (Result, error) {
	plan, err := install.PlanHookInstall(h.ConfigPath(), h.registrations(), matcherGroupIsOurs)
	if err != nil {
		return Result{}, err
	}
	text, err := readTOMLText()
	if err != nil {
		return Result{}, err
	}
	_, flag := EnableFlagText(text)
	return Result{Action: plan.Action, Flag: flag, Hooks: plan}, nil
}

func (h *codexHost) Install() (Result, error) {
	result, err := install.ApplyHookInstall(h.ConfigPath(), h.registrations(), matcherGroupIsOurs)
	if err != nil {
		return Result{}, err
	}
	if result.Action == install.ActionConflict {
		return Result{Action: result.Action, Hooks: result}, nil
	}
	flag, err := ensureCodexFlag()
	if err != nil {
		return Result{}, err
	}
	// A file that already held our entries but had the flag off was NOT
	// installed in any sense that mattered — say what changed.
	return Result{Action: result.Action, Detail: flagDetail(flag), Flag: flag, Hooks: result}, nil
}

func flagDetail(flag string) string {
	switch flag {
	case FlagAlreadyEnabled:
		return ""
	case FlagFlipped:
		return "flipped " + codexFlagLine + " in " + CodexTOMLPath()
	default:
		return "set " + codexFlagLine + " in " + CodexTOMLPath()
	}
}

func (h *codexHost) HasEntry() bool {
	plan, err := install.PlanHookUninstall(h.ConfigPath(), h.registrations(), matcherGroupIsOurs)
	if err != nil {
		return true
	}
	return plan.Action != install.ActionNotInstalled
}

// Uninstall removes our hook entries and deliberately LEAVES `codex_hooks =
// true` alone. It is a host-wide feature flag, not a landfall entry — the user
// may have hooks of their own depending on it, and turning it off would break
// them silently. "Removes only our entries" is meant literally.
func (h *codexHost) Uninstall() (Result, error) {
	result, err := install.ApplyHookUninstall(h.ConfigPath(), h.registrations(), matcherGroupIsOurs)
	if err != nil {
		return Result{}, err
	}
	return Result{Action: result.Action, Hooks: result}, nil
}

// ---------------------------------------------------------------------------
// Cursor — one event, a flatter entry shape, and a schema version stamp
// ---------------------------------------------------------------------------

// Cursor's hook config is its own file with lowercase event names and bare
// {command} entries (no `type`, no matcher group).
//
// It registers `stop` with `--host cursor` and NOTHING ELSE. Cursor's hook
// runtime reads a hook's verdict as JSON on stdout and never sees an exit code
// or a line of stderr, so the bare entry an earlier version wrote could not
// work; that old form is declared superseded in internal/hooks/spec.go so an
// upgrade replaces it in place instead of stranding it as a permanent
// conflict.
//
// WHAT IS DELIBERATELY NOT REGISTERED, AND WHY THAT IS SETTLED:
// `beforeSubmitPrompt`. Its output schema is `{continue}` alone: it can refuse
// a prompt and it cannot inject context, so it cannot deliver a spooled
// digest to the model. The product question that left — drop it, or repurpose
// it as a gate that refuses the HUMAN's next prompt — was DECIDED: drop it. A
// gate that refuses the person typing puts friction on a human to solve an
// agent's problem, and the event carries no channel to explain itself, so the
// refusal would read as the editor silently breaking. Do not add it back.
var cursorEventKey = map[string]string{"stop": "stop"}

type cursorHost struct{}

func (h *cursorHost) ID() string          { return "cursor" }
func (h *cursorHost) DisplayName() string { return "Cursor" }

func (h *cursorHost) ConfigPath() string {
	return filepath.Join(install.HomeDir(), ".cursor", "hooks.json")
}

func (h *cursorHost) Detect() bool {
	for _, c := range install.AppBundleCandidates("Cursor", "cursor") {
		if install.PathExists(c) {
			return true
		}
	}
	return install.PathExists(filepath.Join(install.HomeDir(), ".cursor"))
}

func (h *cursorHost) registrations() []install.Registration {
	var regs []install.Registration
	for _, eventID := range hooks.EventsForHost(h.ID()) {
		key, ok := cursorEventKey[eventID]
		if !ok {
			continue
		}
		var superseded []any
		for _, cmd := range hooks.SupersededHookCommands(eventID, h.ID()) {
			superseded = append(superseded, map[string]any{"command": cmd})
		}
		regs = append(regs, install.Registration{
			KeyPath:    []string{"hooks", key},
			Entry:      map[string]any{"command": hooks.HookCommand(eventID, h.ID())},
			Superseded: superseded,
		})
	}
	return regs
}

func cursorIsOurs(element any) bool {
	m, ok := element.(map[string]any)
	if !ok {
		return false
	}
	cmd, _ := m["command"].(string)
	return hooks.IsLandfallCommand(cmd)
}

func (h *cursorHost) Plan() (Result, error) {
	plan, err := install.PlanHookInstall(h.ConfigPath(), h.registrations(), cursorIsOurs)
	if err != nil {
		return Result{}, err
	}
	return Result{Action: plan.Action, Hooks: plan}, nil
}

func (h *cursorHost) Install() (Result, error) {
	result, err := install.ApplyHookInstall(h.ConfigPath(), h.registrations(), cursorIsOurs)
	if err != nil {
		return Result{}, err
	}
	// Cursor's hook file carries a schema version. Stamp it only on a file we
	// actually wrote, and only when it is ABSENT — never correcting a version a
	// future Cursor (or the user) put there.
	if result.Action == install.ActionConfigured {
		if _, found := result.Data["version"]; !found {
			next := install.SetPath(result.Data, []string{"version"}, 1)
			if err := install.WriteJSONPretty(h.ConfigPath(), next); err != nil {
				return Result{}, err
			}
			result.Data = next
		}
	}
	return Result{Action: result.Action, Hooks: result}, nil
}

func (h *cursorHost) HasEntry() bool {
	plan, err := install.PlanHookUninstall(h.ConfigPath(), h.registrations(), cursorIsOurs)
	if err != nil {
		return true
	}
	return plan.Action != install.ActionNotInstalled
}

func (h *cursorHost) Uninstall() (Result, error) {
	result, err := install.ApplyHookUninstall(h.ConfigPath(), h.registrations(), cursorIsOurs)
	if err != nil {
		return Result{}, err
	}
	return Result{Action: result.Action, Hooks: result}, nil
}
