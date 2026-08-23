// prompt.go — the minimal interactive selection UI for `landfall install`.
// A Go port of `src/install/prompt.mjs`.
//
// A small line-at-a-time per-item confirm, not a full raw-mode checkbox
// widget: it is simple to test (feed it lines) and matches this CLI's
// near-zero-dependency convention.
//
// The Node original goes out of its way to consume stdin as an async ITERATOR
// rather than through repeated `readline.question()` calls, because
// `question()` is unreliable once the underlying stream hits EOF — exactly
// what a piped, non-interactive stdin does the moment its lines are written,
// and a later call could hang forever instead of resolving with an
// already-buffered line. Go has no such hazard: a bufio.Scanner over the
// stream reads buffered lines and then reports EOF, which this treats as the
// same "" answer the Node version's `done` case produces. The behaviour the
// comment protects — every remaining item still gets its default answer after
// input runs out, and nothing blocks — is preserved.
package install

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// SelectItem is one selectable harness/host.
type SelectItem struct {
	ID    string
	Label string
}

// PromptSelection asks the user, one at a time, whether to select each item.
// All are pre-checked: pressing Enter accepts the default, and only an answer
// starting with "n" declines. Returns the selected ids, preserving `items`
// order.
//
// skipPrompt (the `--yes` flag) selects every item with no I/O AT ALL — not
// even a read — which is what lets `--yes` run in a script with no stdin
// attached without hanging.
func PromptSelection(items []SelectItem, skipPrompt bool, in io.Reader, out io.Writer) []string {
	ids := make([]string, 0, len(items))
	if skipPrompt || len(items) == 0 {
		for _, i := range items {
			ids = append(ids, i.ID)
		}
		return ids
	}

	scanner := bufio.NewScanner(in)
	for _, item := range items {
		fmt.Fprintf(out, "Configure %s? [Y/n] ", item.Label)
		answer := ""
		if scanner.Scan() {
			answer = strings.ToLower(strings.TrimSpace(scanner.Text()))
		}
		if !strings.HasPrefix(answer, "n") {
			ids = append(ids, item.ID)
		}
	}
	return ids
}
