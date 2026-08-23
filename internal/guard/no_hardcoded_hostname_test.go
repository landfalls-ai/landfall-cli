package guard

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// loopbackURL matches a port-bearing loopback URL, e.g. http://127.0.0.1:4000
// or http://localhost:3000 — the same class of address instance.mjs's own
// guard forbids outside itself.
var loopbackURL = regexp.MustCompile(`https?://(localhost|127\.0\.0\.1)(:\d+)?`)

func TestNoHardcodedAddress(t *testing.T) {
	for _, path := range moduleGoFiles(t) {
		// The one file allowed to name the hosted instance and reason about
		// loopback addresses is internal/instance — it IS the instance
		// resolution module (contracts/cli-commands.md's Instance resolution
		// precedence; data-model.md's Instance entity).
		if strings.Contains(path, "/internal/instance/") {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		// Strip line comments before checking — a comment explaining the rule
		// (as this file itself does) is not a violation.
		var stripped strings.Builder
		for _, line := range strings.Split(string(data), "\n") {
			if idx := strings.Index(line, "//"); idx >= 0 {
				line = line[:idx]
			}
			stripped.WriteString(line)
			stripped.WriteString("\n")
		}
		content := stripped.String()

		if strings.Contains(content, "landfalls.ai") {
			t.Errorf("%s hardcodes a landfalls.ai hostname — only internal/instance may name the hosted default", path)
		}
		if loc := loopbackURL.FindString(content); loc != "" {
			t.Errorf("%s hardcodes a loopback address (%s) — only internal/instance may reason about loopback URLs", path, loc)
		}
	}
}
