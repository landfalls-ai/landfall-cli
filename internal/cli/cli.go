// Package cli implements the Landfall CLI's command tree.
//
// Execute — the sole entrypoint cmd/landfall/main.go calls — lives in root.go,
// alongside the argv pre-processing that has to happen before Cobra sees an
// argument (the bare-URL-as-serve grammar, global `--link` splicing, and the
// strict argv-anywhere `--help` parity check).
//
// Every command in this package routes ALL output through the ui abstraction
// in ui.go — `ui.Log` for human-readable stderr lines, `ui.Outf` for
// machine-readable stdout — never fmt.Println directly (FR-003). That split is
// not cosmetic: `serve` speaks MCP JSON-RPC on stdout and the hook events
// speak their host's protocol there, so a stray log line on stdout corrupts a
// stream someone is parsing.
package cli
