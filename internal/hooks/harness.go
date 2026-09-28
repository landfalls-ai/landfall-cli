// harness.go — which agent host a front end or a hook process speaks for.
//
// WHY THIS EXISTS (2026-09-28). One person ran Claude Code and Codex in the
// same checkout, joined to the same room. The daemon joined the room once for
// the machine, so both harnesses wrote under ONE agent session labelled
// "edge-agent", each dropped the other's events as its own echo, and one
// harness's Stop hook marked the room read for the other (the terminal reader
// was keyed by workspace alone). A harness is now a first-class part of the
// identity: the daemon gives each one its own agent session in the room, and
// the person's terminal reader is keyed by workspace AND harness.
//
// The front end learns its harness from MCP `initialize` (clientInfo.name). A
// hook process has no MCP handshake, so it infers the same key from what its
// host tells it: `--host` on the command line, then the host's own
// environment and payload (DetectHookHarness).
package hooks

import (
	"encoding/json"
	"strings"
	"unicode"
)

// Harness is one agent host: a stable Key for reader names and hook matching,
// and the Label the room shows for its agent session.
type Harness struct {
	Key   string
	Label string
}

// knownHarnesses maps the names hosts send as MCP clientInfo.name (and the ids
// `landfall hooks install` writes) to one key and one display label. Matched on
// the lowercased name, exactly or as a prefix where a host appends a variant
// ("cursor-vscode", "Visual Studio Code - Insiders").
var knownHarnesses = []struct {
	names  []string
	prefix bool
	h      Harness
}{
	{names: []string{"claude-code", "claude code"}, h: Harness{Key: "claude-code", Label: "Claude Code"}},
	{names: []string{"codex-mcp-client", "codex"}, h: Harness{Key: "codex", Label: "Codex"}},
	{names: []string{"cursor"}, prefix: true, h: Harness{Key: "cursor", Label: "Cursor"}},
	{names: []string{"visual studio code", "vscode"}, prefix: true, h: Harness{Key: "vscode", Label: "VS Code"}},
	{names: []string{"windsurf"}, prefix: true, h: Harness{Key: "windsurf", Label: "Windsurf"}},
}

// harnessNameMax bounds a label taken from a name no table knows.
const harnessNameMax = 40

// HarnessFromClientName turns an MCP clientInfo.name (or a hook host id) into
// a Harness. A name the table does not know is kept, sanitized, as both key
// and label; an empty or unusable name is the zero Harness ("unknown").
func HarnessFromClientName(name string) Harness {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" {
		return Harness{}
	}
	for _, k := range knownHarnesses {
		for _, n := range k.names {
			if lower == n || (k.prefix && strings.HasPrefix(lower, n)) {
				return k.h
			}
		}
	}
	label := sanitizeLabel(name)
	key := harnessKey(lower)
	if key == "" || label == "" {
		return Harness{}
	}
	return Harness{Key: key, Label: label}
}

// sanitizeLabel keeps letters, digits, spaces and a little punctuation, so a
// host's self-description cannot put control characters or markup into a
// name every participant sees.
func sanitizeLabel(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	if r := []rune(out); len(r) > harnessNameMax {
		out = strings.TrimSpace(string(r[:harnessNameMax]))
	}
	return out
}

// harnessKey is a lowercase [a-z0-9-] slug: safe inside a reader name.
func harnessKey(lower string) string {
	var b strings.Builder
	dash := false
	for _, r := range lower {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if len(out) > harnessNameMax {
		out = strings.TrimRight(out[:harnessNameMax], "-")
	}
	return out
}

// codexEnvHints are variables Codex sets in the environment of what it runs.
// Any one of them, with no Claude Code marker, reads as Codex.
var codexEnvHints = []string{"CODEX_THREAD_ID", "CODEX_SANDBOX", "CODEX_SANDBOX_NETWORK_DISABLED", "CODEX_MANAGED_BY_NPM", "CODEX_MANAGED_BY_BUN"}

// DetectHookHarness is the harness key a hook process speaks for, or "" when
// nothing says. In order:
//
//  1. `--host <id>` on the command line (the installer writes it for Cursor);
//  2. LANDFALL_HARNESS, an explicit override for any host;
//  3. CLAUDECODE=1, which Claude Code sets for everything it runs;
//  4. the payload's transcript_path, under ~/.codex/ or ~/.claude/;
//  5. an environment variable only Codex sets.
//
// "" is not an error: the daemon then answers for this workspace's harness-less
// reader, or for all of the workspace's terminal readers together, which is
// how every hook behaved before harnesses were told apart.
func DetectHookHarness(hostFlag, input string, getenv func(string) string) string {
	if h := HarnessFromClientName(hostFlag); h.Key != "" {
		return h.Key
	}
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	if h := HarnessFromClientName(getenv("LANDFALL_HARNESS")); h.Key != "" {
		return h.Key
	}
	if getenv("CLAUDECODE") == "1" {
		return "claude-code"
	}
	if input != "" {
		var p struct {
			TranscriptPath  string `json:"transcript_path"`
			TranscriptPath2 string `json:"transcriptPath"`
		}
		if json.Unmarshal([]byte(input), &p) == nil {
			tp := p.TranscriptPath
			if tp == "" {
				tp = p.TranscriptPath2
			}
			switch {
			case strings.Contains(tp, "/.codex/"):
				return "codex"
			case strings.Contains(tp, "/.claude/"):
				return "claude-code"
			}
		}
	}
	for _, k := range codexEnvHints {
		if getenv(k) != "" {
			return "codex"
		}
	}
	return ""
}

// TerminalReaderNameFor is the daemon's reader for the person at a checkout in
// one harness. With no harness it is the harness-less name every build before
// this one used (TerminalReaderName), so an unidentified front end and an
// unidentified hook still meet.
func TerminalReaderNameFor(workspaceKey, harness string) string {
	if harness == "" {
		return TerminalReaderName(workspaceKey)
	}
	return TerminalReaderName(workspaceKey) + ":" + harness
}
