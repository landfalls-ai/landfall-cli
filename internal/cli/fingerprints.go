package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/landfalls-ai/landfall-cli/internal/daemon"
)

// computeFingerprints is what the front end tells the daemon about the
// person's working directory at attach, so the daemon can recognise a share
// that names it (spec FR-008, research R4): tracked paths, recent commit
// hashes, the directory's name and the manifest's package name. Read-only,
// local, capped, and empty (never an error) for a directory that is not a git
// repository — the hold then simply has nothing to match.
func computeFingerprints(dir string) *daemon.Fingerprints {
	fp := &daemon.Fingerprints{}
	if base := filepath.Base(dir); base != "" && base != "." && base != "/" {
		fp.Names = append(fp.Names, base)
	}
	if body, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		var pkg struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(body, &pkg) == nil && pkg.Name != "" && pkg.Name != fp.Names[0] {
			fp.Names = append(fp.Names, pkg.Name)
		}
	}
	if out, err := git(dir, "ls-files"); err == nil {
		for _, p := range strings.Split(strings.TrimSpace(out), "\n") {
			if p == "" {
				continue
			}
			fp.Paths = append(fp.Paths, p)
			if len(fp.Paths) >= 5000 {
				break
			}
		}
	}
	if out, err := git(dir, "log", "-50", "--format=%H %h"); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			parts := strings.Fields(line)
			fp.Commits = append(fp.Commits, parts...)
		}
	}
	return fp
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	return string(out), err
}
