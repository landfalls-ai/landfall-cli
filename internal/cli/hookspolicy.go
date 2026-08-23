package cli

// hookspolicy.go — `landfall hooks policy [--init]`, a port of
// `runHooksPolicy` in `src/hooks/commands.mjs`.
//
// This command is not a convenience. #233's acceptance criterion is that "the
// engineer can read exactly what leaves the machine", and a policy file plus a
// promise about how it is interpreted does not satisfy that — the interpreter
// has to show its work. Because a rule's payload is built only from literals in
// the rule (see internal/hooks/policy.go, and IntentFor's deliberate lack of a
// command-line parameter), what this prints IS the complete set of values this
// machine can ever send, with no caveats about what a matched command might add.
//
// `--init` writes a documented starter file, and refuses to touch an existing
// one: overwriting a policy someone tuned would be the worst possible outcome of
// a command that reads like an explanation.
//
// It prints to STDOUT, like every other reporting subcommand — it is the answer
// to "what leaves my machine?", not a log line.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/hooks"
)

// HooksPolicyDeps are the command's injectable effects.
type HooksPolicyDeps struct {
	// Load reads and validates the policy. Nil means hooks.LoadPolicy.
	Load func(filePath string) hooks.Policy
	// FilePath is the policy file. Empty means hooks.PolicyPath().
	FilePath string
	// Now is the timestamp IntentFor is handed. It never reaches the output —
	// the printed `startedAt` is the literal "<when you confirm>" — but the
	// parameter is threaded so the rendering path is the same one production
	// takes.
	Now time.Time
}

// RunHooksPolicy is the command body: the lines to print and the exit code.
//
// Exit 1 when any rule was rejected — a policy the engineer believes covers them
// and does not is exactly what this command exists to surface, so it must not
// look like a success in a script.
func RunHooksPolicy(argv []string, deps HooksPolicyDeps) (lines []string, exitCode int) {
	filePath := deps.FilePath
	if filePath == "" {
		filePath = hooks.PolicyPath()
	}
	load := deps.Load
	if load == nil {
		load = hooks.LoadPolicy
	}

	wantInit := false
	for _, a := range argv {
		if a == "--init" {
			wantInit = true
			break
		}
	}

	if wantInit {
		if _, err := os.Stat(filePath); err == nil {
			lines = append(lines, "policy already exists: "+filePath+" (not overwritten)")
		} else {
			if err := writeStarterPolicy(filePath); err != nil {
				lines = append(lines, "could not write "+filePath+": "+err.Error())
				return lines, 1
			}
			lines = append(lines, "wrote starter policy: "+filePath)
			lines = append(lines, "edit it, then re-run `landfall hooks policy` to see what each rule would send.")
			return lines, 0
		}
	}

	policy := load(filePath)
	lines = append(lines, "policy: "+filePath)
	if !policy.Exists {
		lines = append(lines, "  (no policy file — no command on this machine is reported; `--init` writes a starter)")
		return lines, 0
	}
	for _, e := range policy.Errors {
		lines = append(lines, "  ! "+e)
	}
	if len(policy.Rules) == 0 {
		lines = append(lines, "  (no usable rules — nothing is reported)")
		return lines, exitCodeForPolicyErrors(policy)
	}

	for _, rule := range policy.Rules {
		lines = append(lines, "")
		head := "  " + rule.ID
		if rule.Description != "" {
			head += " — " + rule.Description
		}
		lines = append(lines, head)

		matches := "    matches: " + rule.Command
		if len(rule.AllOf) > 0 {
			matches += " containing all of " + quotedList(rule.AllOf)
		} else {
			matches += " (any invocation)"
		}
		lines = append(lines, matches)
		if len(rule.NoneOf) > 0 {
			lines = append(lines, "    unless it contains any of "+quotedList(rule.NoneOf))
		}

		// The whole point of the command: the EXACT body, with only the one field
		// that cannot be known ahead of time standing in for itself.
		intent := hooks.IntentFor(rule, deps.Now)
		intent.StartedAt = "<when you confirm>"
		lines = append(lines, "    sends: "+jsonLine(intent, "{}"))
	}
	lines = append(lines, "")
	lines = append(lines, "Nothing else is sent. The command line itself never leaves this machine.")
	return lines, exitCodeForPolicyErrors(policy)
}

func exitCodeForPolicyErrors(p hooks.Policy) int {
	if len(p.Errors) > 0 {
		return 1
	}
	return 0
}

// quotedList is `list.map(JSON.stringify).join(', ')`.
func quotedList(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = jsonLine(v, `""`)
	}
	return strings.Join(quoted, ", ")
}

// jsonLine is `JSON.stringify(value)` on one line, with Go's HTML escaping OFF.
//
// The escaping is not cosmetic here. This command's whole claim is that what it
// prints IS what leaves the machine, and Go's default encoder rewrites the three
// HTML-significant bytes: the placeholder would print as
// `<when you confirm>`, and a rule's own literal containing `&` would
// come out re-encoded. Either makes the printed body something other than the
// body, which is precisely the gap the command exists to close.
func jsonLine(value any, fallback string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return fallback
	}
	return strings.TrimRight(buf.String(), "\n")
}

// writeStarterPolicy writes the documented starter, 0600 in a 0700 directory —
// the same treatment the cached session next to it gets.
func writeStarterPolicy(filePath string) error {
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filePath, hooks.StarterPolicyJSON(), 0o600); err != nil {
		return err
	}
	return nil
}

// runHooksPolicyCommand prints the lines and carries the exit code out.
func runHooksPolicyCommand(ui *UI, argv []string) error {
	lines, exitCode := RunHooksPolicy(argv, HooksPolicyDeps{})
	for _, line := range lines {
		ui.Outf("%s\n", line)
	}
	if exitCode != 0 {
		return &exitError{code: exitCode}
	}
	return nil
}
