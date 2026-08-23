// Package guard holds structural lint-rules-as-tests, ported from the Node
// CLI's test/no-monorepo-coupling.test.mjs and test/no-hardcoded-address.test.mjs.
// Neither is a behavior test — both guard an invariant the CLI's own
// extraction (feature 050) depends on: this module must never grow a
// dependency back into the private monorepo it was pulled out of.
package guard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// moduleGoFiles walks the repo root (two levels up from this test file) and
// returns every .go file under it, excluding vendor/testdata-style dirs.
func moduleGoFiles(t *testing.T) []string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolving module root: %v", err)
	}
	var files []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "src", "test", "docs", "bin", "agents", ".claude-plugin", "guard":
				// src/ and test/ are the pre-rewrite Node source being replaced in
				// place over the course of this port — they are expected to
				// reference the old layout until they're deleted, and are not
				// part of the Go module. guard/ (this package) is excluded from
				// itself: it necessarily contains the forbidden strings as string
				// literals to check against, which would otherwise false-positive.
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking module root: %v", err)
	}
	return files
}

func TestNoMonorepoCoupling(t *testing.T) {
	forbidden := []string{"libs/", "apps/", "../../.."}
	for _, path := range moduleGoFiles(t) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		content := string(data)
		for _, f := range forbidden {
			if strings.Contains(content, f) {
				t.Errorf("%s references %q — this module must never depend on the monorepo it was extracted from (feature 050)", path, f)
			}
		}
	}
}
