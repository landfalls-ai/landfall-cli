// report.go — the Installation Report: the single place that turns a slice of
// per-harness (or per-hook-host) Outcomes into the exact output shape the CLI
// contract promises, and the exit code that goes with it. A Go port of
// `src/install/report.mjs`.
package install

import "strings"

// InstallStatuses is every status a `landfall install` outcome can carry.
var InstallStatuses = []string{
	"not-detected",
	"configured",
	"would-configure", // --dry-run stand-in for `configured`
	"already-installed",
	"skipped", // user did not select a detected harness
	"conflict",
	"failed",
}

// UninstallStatuses is every status a `landfall uninstall` outcome can carry.
var UninstallStatuses = []string{
	"not-installed",
	"removed",
	"left-in-place",
	"failed",
}

// FormatOutcomeLine renders one Outcome as the contract's single output line:
//
//	<display-name>: <status>[ — <detail>][ (<config-path>)][ [plugin: <status>]]
//
// PluginStatus is Claude-Code-only and empty on every other harness's outcome,
// so this is purely additive — it changes nothing for the five harnesses that
// never set it.
func FormatOutcomeLine(o Outcome) string {
	var b strings.Builder
	b.WriteString(o.DisplayName)
	b.WriteString(": ")
	b.WriteString(o.Status)
	if o.Detail != "" {
		b.WriteString(" — ")
		b.WriteString(o.Detail)
	}
	if o.ConfigPath != "" {
		b.WriteString(" (")
		b.WriteString(o.ConfigPath)
		b.WriteString(")")
	}
	if o.PluginStatus != "" {
		b.WriteString(" [plugin: ")
		b.WriteString(o.PluginStatus)
		b.WriteString("]")
	}
	return b.String()
}

// FormatReport renders the full Installation Report, one line per outcome, in
// the given order.
func FormatReport(outcomes []Outcome) string {
	lines := make([]string, 0, len(outcomes))
	for _, o := range outcomes {
		lines = append(lines, FormatOutcomeLine(o))
	}
	return strings.Join(lines, "\n")
}

// ExitCodeForOutcomes is the exit-code convention: 1 if any outcome is
// `failed`, else 0. Usage errors (a bad --only value, an aborted sign-in) are
// a separate, EARLIER exit 2 the caller raises directly — this only looks at
// completed outcomes, so a `conflict` is a report and not a failure.
func ExitCodeForOutcomes(outcomes []Outcome) int {
	for _, o := range outcomes {
		if o.Status == "failed" {
			return 1
		}
	}
	return 0
}
