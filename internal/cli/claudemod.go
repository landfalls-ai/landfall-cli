package cli

// claudemod.go — `landfall hooks install` also installs the Landfall mod for
// Claude Code (plugins/claude-code) when the Claude Code on this machine can
// load one.
//
// A mod draws the room in the session while it is idle, which no settings hook
// can (plugins/claude-code/README.md). It is installed IN ADDITION to the
// settings hooks, never instead of them: an older Claude Code, Codex, Cursor,
// and an organization that sets allowManagedModsOnly all keep using the hooks,
// and where the mod does load it tells them to stand down (LANDFALL_MOD,
// hookevents.go modOwnsEvent).
//
// Everything goes through Claude Code's own `claude plugin` commands, so the
// person can see and remove it with `/plugin` like any other plugin:
//
//	claude plugin marketplace add landfalls-ai/landfall-cli   (once)
//	claude plugin marketplace update landfall                 (when already added)
//	claude plugin install landfall@landfall --scope user
//
// LANDFALL_CLAUDE_MOD=0 skips the mod; LANDFALL_MARKETPLACE names another
// marketplace source (a local checkout, to try a branch before it is released).

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/install"
)

// ClaudeModID is the plugin's install id: plugin `landfall` in the
// marketplace `landfall` (.claude-plugin/marketplace.json).
const ClaudeModID = "landfall@landfall"

// claudeModDisplayName is how the outcome is listed.
const claudeModDisplayName = "Claude Code mod"

// MinClaudeModVersion is the first Claude Code that loads mods by default.
var MinClaudeModVersion = [3]int{2, 1, 287}

// defaultMarketplace is where the mod is published.
const defaultMarketplace = "landfalls-ai/landfall-cli"

// ClaudeMod is what the installer needs from Claude Code, injectable for tests.
type ClaudeMod interface {
	// Version is `claude --version`'s x.y.z.
	Version() (string, error)
	// Installed reports whether the mod is installed and enabled for the user.
	Installed() (bool, error)
	Install() error
	Uninstall() error
}

// execClaudeMod runs the real `claude`.
type execClaudeMod struct {
	bin         string
	marketplace string
}

func newExecClaudeMod(getenv func(string) string) execClaudeMod {
	m := execClaudeMod{bin: "claude", marketplace: defaultMarketplace}
	if v := getenv("LANDFALL_MARKETPLACE"); v != "" {
		m.marketplace = v
	}
	return m
}

func (m execClaudeMod) run(args ...string) (string, error) {
	cmd := exec.Command(m.bin, args...)
	cmd.Stdin = nil
	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		out, err = cmd.CombinedOutput()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Minute):
		_ = cmd.Process.Kill()
		<-done
		return string(out), fmt.Errorf("`claude %s` did not finish in 2 minutes", strings.Join(args, " "))
	}
	if err != nil {
		why := strings.TrimSpace(lastLine(string(out)))
		if why == "" {
			why = err.Error()
		}
		return string(out), fmt.Errorf("`claude %s` failed: %s", strings.Join(args, " "), why)
	}
	return string(out), nil
}

var versionRe = regexp.MustCompile(`\b(\d+)\.(\d+)\.(\d+)\b`)

func (m execClaudeMod) Version() (string, error) {
	out, err := m.run("--version")
	if err != nil {
		return "", err
	}
	v := versionRe.FindString(out)
	if v == "" {
		return "", errors.New("could not read a version from `claude --version`")
	}
	return v, nil
}

func (m execClaudeMod) Installed() (bool, error) {
	out, err := m.run("plugin", "list", "--json")
	if err != nil {
		return false, err
	}
	var list []struct {
		ID      string `json:"id"`
		Scope   string `json:"scope"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.Unmarshal([]byte(jsonPart(out)), &list); err != nil {
		return false, fmt.Errorf("could not read `claude plugin list --json`: %w", err)
	}
	for _, p := range list {
		if p.ID == ClaudeModID && p.Scope == "user" && p.Enabled {
			return true, nil
		}
	}
	return false, nil
}

func (m execClaudeMod) Install() error {
	out, err := m.run("plugin", "marketplace", "list", "--json")
	if err != nil {
		return err
	}
	var markets []struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal([]byte(jsonPart(out)), &markets)
	have := false
	for _, mk := range markets {
		if mk.Name == "landfall" {
			have = true
		}
	}
	if have {
		// Pull the catalog, so a marketplace added before the mod existed lists it.
		if _, err := m.run("plugin", "marketplace", "update", "landfall"); err != nil {
			return err
		}
	} else if _, err := m.run("plugin", "marketplace", "add", m.marketplace, "--scope", "user"); err != nil {
		return err
	}
	_, err = m.run("plugin", "install", ClaudeModID, "--scope", "user")
	return err
}

func (m execClaudeMod) Uninstall() error {
	_, err := m.run("plugin", "uninstall", ClaudeModID)
	return err
}

// jsonPart is the output from its first [ or {, so a warning line Claude Code
// prints ahead of the JSON does not break the parse.
func jsonPart(out string) string {
	if i := strings.IndexAny(out, "[{"); i >= 0 {
		return out[i:]
	}
	return out
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// versionAtLeast compares x.y.z against min.
func versionAtLeast(v string, min [3]int) bool {
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return false
	}
	for i := 0; i < 3; i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return false
		}
		if n != min[i] {
			return n > min[i]
		}
	}
	return true
}

func minVersionString() string {
	return fmt.Sprintf("%d.%d.%d", MinClaudeModVersion[0], MinClaudeModVersion[1], MinClaudeModVersion[2])
}

// installClaudeMod is the mod's outcome for `hooks install`.
func installClaudeMod(mod ClaudeMod, dryRun bool) install.Outcome {
	o := install.Outcome{DisplayName: claudeModDisplayName}
	v, err := mod.Version()
	if err != nil {
		o.Status, o.Detail = "skipped", err.Error()
		return o
	}
	if !versionAtLeast(v, MinClaudeModVersion) {
		o.Status = "skipped"
		o.Detail = "Claude Code " + v + " predates mods (" + minVersionString() + "); the hooks above cover it. Update Claude Code and run this again."
		return o
	}
	installed, err := mod.Installed()
	if err != nil {
		o.Status, o.Detail = "failed", err.Error()
		return o
	}
	if installed {
		o.Status, o.Detail = "already-installed", ClaudeModID
		return o
	}
	if dryRun {
		o.Status, o.Detail = "would-configure", ClaudeModID
		return o
	}
	if err := mod.Install(); err != nil {
		o.Status, o.Detail = "failed", err.Error()
		return o
	}
	o.Status = "configured"
	o.Detail = ClaudeModID + ": the room shows above the prompt in new Claude Code sessions (run /reload-plugins in an open one)"
	return o
}

// uninstallClaudeMod removes the mod if it is installed; nothing otherwise.
func uninstallClaudeMod(mod ClaudeMod) (install.Outcome, bool) {
	installed, err := mod.Installed()
	if err != nil || !installed {
		return install.Outcome{}, false
	}
	o := install.Outcome{DisplayName: claudeModDisplayName}
	if err := mod.Uninstall(); err != nil {
		o.Status, o.Detail = "failed", err.Error()
		return o, true
	}
	o.Status, o.Detail = "removed", ClaudeModID
	return o, true
}

// claudeModEnabled is false when the person opted out with LANDFALL_CLAUDE_MOD=0.
func claudeModEnabled(getenv func(string) string) bool {
	return getenv("LANDFALL_CLAUDE_MOD") != "0"
}
