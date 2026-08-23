// platform.go — small, shared OS-probing helpers used by every harness
// adapter's Detect()/config-path resolution, and by the hook hosts in
// internal/hooks/hosts. A Go port of `src/install/platform.mjs`.
//
// Deliberately re-reads env/os state on every call rather than caching at
// package-init time, so tests can redirect HOME/PATH per-sandbox exactly the
// way test/install/helpers.mjs does today (`t.Setenv` + a temp tree).
package install

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// IsOnPath reports whether `bin` resolves on the current PATH.
//
// `src/install/platform.mjs:14-22` shells out to `which`/`where` for this.
// exec.LookPath answers the same question by reading $PATH itself (and, on
// Windows, %PATHEXT%) without spawning a process — the subprocess in the Node
// version is an implementation detail of not having a lookup primitive, not a
// behavior anyone depends on.
func IsOnPath(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

// PathExists reports whether a filesystem path exists (file OR directory).
func PathExists(candidate string) bool {
	_, err := os.Stat(candidate)
	return err == nil
}

// HomeDir is the current user's home directory (re-read per call — see the
// file header). Returns "" if it cannot be determined, which every caller
// then turns into a relative path that simply will not exist — the same inert
// outcome Node's `os.homedir()` throwing would produce, without the panic.
func HomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// AppDataDir is the Windows roaming AppData root, honoring $APPDATA when set
// (which the tests do).
func AppDataDir() string {
	if v := os.Getenv("APPDATA"); v != "" {
		return v
	}
	return filepath.Join(HomeDir(), "AppData", "Roaming")
}

// XDGConfigHome is the XDG config home, honoring $XDG_CONFIG_HOME when set.
func XDGConfigHome() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v
	}
	return filepath.Join(HomeDir(), ".config")
}

// VSCodeUserDir is the user-level VS Code config directory, per OS (VS Code's
// own convention).
func VSCodeUserDir() string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(HomeDir(), "Library", "Application Support", "Code", "User")
	case "windows":
		return filepath.Join(AppDataDir(), "Code", "User")
	default:
		return filepath.Join(XDGConfigHome(), "Code", "User")
	}
}

// AppBundleCandidates is the app-bundle / install-marker paths to probe for a
// GUI app, per OS.
//
// LANDFALL_TEST_APP_ROOT, when set, replaces the hardcoded `/Applications`
// (or `Program Files`) root — the ONLY way tests can sandbox this check, since
// these are fixed OS install locations with no per-user env var of their own
// (unlike HOME-relative paths, which the test env already redirects). Never
// set outside a test process.
func AppBundleCandidates(macApp, winDirName string) []string {
	testRoot := os.Getenv("LANDFALL_TEST_APP_ROOT")
	switch runtime.GOOS {
	case "darwin":
		root := testRoot
		if root == "" {
			root = "/Applications"
		}
		return []string{filepath.Join(root, macApp+".app")}
	case "windows":
		local, program := testRoot, testRoot
		if local == "" {
			local = filepath.Join(AppDataDir(), "..", "Local", "Programs")
		}
		if program == "" {
			program = filepath.Join("C:", "Program Files")
		}
		return []string{
			filepath.Join(local, winDirName),
			filepath.Join(program, winDirName),
		}
	default:
		return nil
	}
}
