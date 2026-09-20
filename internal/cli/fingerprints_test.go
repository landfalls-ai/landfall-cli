package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestComputeFingerprintsReadsPathsCommitsAndNamesFromAGitRepo(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "src"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "src", "cache.js"), []byte("const TTL_MS = 60_000;\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"checkout-service"}`), 0o644)
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q")
	run("add", "-A")
	run("commit", "-q", "-m", "skeleton")

	fp := computeFingerprints(dir)
	has := func(list []string, want string) bool {
		for _, s := range list {
			if s == want {
				return true
			}
		}
		return false
	}
	if !has(fp.Paths, "src/cache.js") || !has(fp.Paths, "package.json") {
		t.Fatalf("paths = %v", fp.Paths)
	}
	if len(fp.Commits) != 2 || len(fp.Commits[0]) != 40 || len(fp.Commits[1]) < 7 {
		t.Fatalf("commits = %v, want one full and one short hash", fp.Commits)
	}
	if !has(fp.Names, "checkout-service") || !has(fp.Names, filepath.Base(dir)) {
		t.Fatalf("names = %v", fp.Names)
	}
}

func TestComputeFingerprintsIsEmptyOutsideARepo(t *testing.T) {
	fp := computeFingerprints(t.TempDir())
	if len(fp.Paths) != 0 || len(fp.Commits) != 0 {
		t.Fatalf("a plain directory has nothing to fingerprint: %+v", fp)
	}
}
