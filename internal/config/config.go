// Package config owns the CLI's persisted, non-secret configuration file —
// `$XDG_CONFIG_HOME/landfall/config.json` — and nothing else.
//
// Scope is deliberately narrow (T011, research.md §B). Viper is used HERE and
// only here, as a plain data source: it reads and writes the config file, and
// that is the whole of its job. It does NOT decide precedence.
//
// Viper's own flag > env > config > default order is the WRONG order for this
// CLI: the per-endpoint environment variables (LANDFALL_WEB_URL /
// LANDFALL_BASE_URL) must outrank an explicit `--url` flag, which Viper cannot
// express natively. So `internal/instance.Resolve` keeps a hand-written
// precedence chain and merely asks this package "what, if anything, is on
// disk?".
//
// Viper also never touches credentials or runtime state. Those live in
// `internal/auth` with their own 0600 discipline — a config library that
// helpfully merges env vars and flags is exactly what you do not want anywhere
// near a bearer token.
//
// The dependency direction is config <- instance (instance imports config).
// It is never the other way around: the hosted-default hostnames and address
// validation belong solely to internal/instance, and importing this package
// from there would otherwise be a cycle.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
)

// FileName is the persisted config file's basename. Deliberately separate from
// credentials.json: signing out must not discard a self-hoster's choice of
// which Landfall they are talking to.
const FileName = "config.json"

// Warn reports a recoverable configuration problem. Package-level so a test
// can capture it; it writes to stderr because stdout is reserved for
// machine-readable output (FR-003).
var Warn = func(msg string) {
	fmt.Fprintln(os.Stderr, "[landfall] "+msg)
}

// Dir is the CLI's configuration directory, honouring XDG_CONFIG_HOME and
// falling back to ~/.config. Resolved on every call rather than cached, because
// tests (and `env XDG_CONFIG_HOME=... landfall ...`) change it between calls.
func Dir() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			// Nothing better to do than a relative path; every caller of this
			// treats a read failure as "absent", so this degrades to the
			// built-in defaults rather than crashing.
			home = "."
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "landfall")
}

// Path is the full path to config.json.
func Path() string {
	return filepath.Join(Dir(), FileName)
}

// Nomination is the persisted answer to "which Landfall am I pointed at?".
//
// Every field is optional: a nomination naming only `web` is valid and is
// completed from the level below by internal/instance, never left undefined.
// This type carries no validation of its own on purpose — address parsing is
// internal/instance's job, and doing it here would duplicate the one place
// allowed to reason about addresses.
type Nomination struct {
	Name string `mapstructure:"name" json:"name,omitempty"`
	Web  string `mapstructure:"web"  json:"web,omitempty"`
	API  string `mapstructure:"api"  json:"api,omitempty"`
	Docs string `mapstructure:"docs" json:"docs,omitempty"`
}

// LoadNomination reads the persisted nomination, or returns nil when there is
// none.
//
// A broken file is treated as ABSENT and says so once, rather than making every
// command fail until someone finds and deletes it. That is the Node source's
// behaviour (src/instance.mjs readNomination) and it matters: a corrupt
// config.json during an incident must not be the reason the CLI stops working.
func LoadNomination() *Nomination {
	path := Path()
	if _, err := os.Stat(path); err != nil {
		return nil // absent is the normal case, not an error
	}

	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("json")
	if err := v.ReadInConfig(); err != nil {
		Warn(fmt.Sprintf(
			"Ignoring %s: it is not valid JSON. Run `landfall instance reset` to clear it.", path))
		return nil
	}

	// `instance` must be present AND be an object. A file shaped
	// `{"instance": 3}` is as good as absent.
	raw := v.Get("instance")
	if raw == nil {
		return nil
	}
	if _, ok := raw.(map[string]any); !ok {
		return nil
	}

	var n Nomination
	if err := v.UnmarshalKey("instance", &n); err != nil {
		return nil
	}
	if n.Name == "" && n.Web == "" && n.API == "" && n.Docs == "" {
		return nil
	}
	return &n
}

// SaveNomination persists a nomination, creating the config directory if
// needed.
//
// Empty fields are omitted rather than materialised as empty strings: a
// nomination naming only `web` must round-trip as a nomination naming only
// `web`, since internal/instance distinguishes "absent, complete it from the
// level below" from "present and empty".
func SaveNomination(n Nomination) error {
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", Dir(), err)
	}

	fields := map[string]any{}
	for key, value := range map[string]string{
		"name": n.Name, "web": n.Web, "api": n.API, "docs": n.Docs,
	} {
		if value != "" {
			fields[key] = value
		}
	}

	v := viper.New()
	v.SetConfigFile(Path())
	v.SetConfigType("json")
	v.Set("instance", fields)
	if err := v.WriteConfigAs(Path()); err != nil {
		return fmt.Errorf("writing %s: %w", Path(), err)
	}
	return nil
}

// ClearNomination removes the persisted nomination, returning true when a file
// was actually removed. Returning to the built-in default must be possible
// without uninstalling or hand-editing a file.
func ClearNomination() bool {
	if err := os.Remove(Path()); err != nil {
		return false // already absent
	}
	return true
}
