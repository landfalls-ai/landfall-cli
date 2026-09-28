package narrate

// catalog.go: get_signal_catalog's answer, as something an agent reads rather
// than parses.
//
// The route answers with one JSON object per connected source (its kinds, the
// connection it is, and the read operations it advertises, each with a
// description and per-parameter hints). The tool used to print that array
// verbatim, indented: every key quoted, every hint on its own line, a dozen
// lines per operation. This keeps everything query_signals needs (the source,
// the connection and account to pass, each operation's name, what it reads,
// its parameters in the order the source declared them, and how it takes a
// time window) on a few lines per operation, and drops only what no agent
// acts on (the IAM actions and permission tier an AWS operation declares for
// the onboarding policy generator).
//
// Parsed leniently: the entry shape is the source plugin's, not this CLI's. A
// field this renderer does not know is ignored; an entry it cannot read at all
// is shown as its raw JSON, never dropped.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

type catalogEntry struct {
	Source       string             `json:"source"`
	Kinds        []string           `json:"kinds"`
	ConnectionID string             `json:"connectionId"`
	Label        string             `json:"label"`
	AccountID    string             `json:"accountId"`
	ContextHint  string             `json:"contextHint"`
	Operations   []catalogOperation `json:"operations"`
}

type catalogOperation struct {
	Operation   string         `json:"operation"`
	Description string         `json:"description"`
	Kind        string         `json:"kind"`
	Canonical   string         `json:"canonical"`
	Params      orderedHints   `json:"params"`
	Window      *catalogWindow `json:"window"`
}

type catalogWindow struct {
	Start  string `json:"start"`
	End    string `json:"end"`
	Step   string `json:"step"`
	Format string `json:"format"`
}

// orderedHints is a JSON object of parameter hints kept in the order the
// source declared them: "Namespace, MetricName, StartTime" reads as the
// source meant it; alphabetical does not.
type orderedHints []hint

type hint struct{ name, text string }

func (o *orderedHints) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		// Not an object (null, or a shape this renderer does not know): no hints.
		return nil
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		key, _ := keyTok.(string)
		var v any
		if err := dec.Decode(&v); err != nil {
			return err
		}
		text, isStr := v.(string)
		if !isStr {
			raw, _ := json.Marshal(v)
			text = string(raw)
		}
		*o = append(*o, hint{name: key, text: text})
	}
	return nil
}

// catalogLineMax bounds one description or hint: a line, not a document.
const catalogLineMax = 200

// RenderSignalCatalog renders get_signal_catalog's answer.
func RenderSignalCatalog(entries []client.SignalCatalogEntry) string {
	if len(entries) == 0 {
		return "No telemetry sources are connected for this incident's organization, so there is nothing to query."
	}

	perSource := map[string]int{}
	parsed := make([]*catalogEntry, len(entries))
	for i, raw := range entries {
		var e catalogEntry
		if err := json.Unmarshal(raw, &e); err == nil && e.Source != "" {
			parsed[i] = &e
			perSource[e.Source]++
		}
	}

	var sb strings.Builder
	noun := "sources"
	if len(entries) == 1 {
		noun = "source"
	}
	fmt.Fprintf(&sb, "%d telemetry %s connected. Read one with query_signals {source, operation, params}", len(entries), noun)
	if hasRepeats(perSource) {
		sb.WriteString("; where a source is listed more than once, also pass the connection (and account) shown for the one you mean")
	}
	sb.WriteString(".\n")

	for i, e := range parsed {
		sb.WriteString("\n")
		if e == nil {
			fmt.Fprintf(&sb, "(an entry this CLI could not read, as sent) %s\n", oneLine(string(entries[i]), 400))
			continue
		}
		renderCatalogEntry(&sb, e)
	}
	return strings.TrimRight(sb.String(), "\n")
}

func hasRepeats(perSource map[string]int) bool {
	for _, n := range perSource {
		if n > 1 {
			return true
		}
	}
	return false
}

func renderCatalogEntry(sb *strings.Builder, e *catalogEntry) {
	head := []string{e.Source}
	if len(e.Kinds) > 0 {
		head = append(head, strings.Join(e.Kinds, ", "))
	}
	if e.ConnectionID != "" {
		conn := "connection " + quoteText(e.ConnectionID)
		if e.Label != "" && e.Label != e.ConnectionID {
			conn += " (" + oneLine(e.Label, 80) + ")"
		}
		head = append(head, conn)
	} else if e.Label != "" {
		head = append(head, oneLine(e.Label, 80))
	}
	if e.AccountID != "" {
		head = append(head, "account "+quoteText(e.AccountID))
	}
	sb.WriteString(strings.Join(head, " · "))
	sb.WriteString("\n")

	if len(e.Operations) == 0 {
		sb.WriteString("  (advertises no operations)\n")
	}
	for _, op := range e.Operations {
		tags := []string{}
		if op.Kind != "" {
			tags = append(tags, op.Kind)
		}
		if op.Canonical != "" {
			tags = append(tags, op.Canonical)
		}
		line := "  " + op.Operation
		if len(tags) > 0 {
			line += " (" + strings.Join(tags, ", ") + ")"
		}
		if d := oneLine(op.Description, catalogLineMax); d != "" {
			line += ": " + d
		}
		sb.WriteString(line + "\n")
		if len(op.Params) > 0 {
			parts := make([]string, 0, len(op.Params))
			for _, h := range op.Params {
				if t := oneLine(h.text, catalogLineMax); t != "" {
					parts = append(parts, h.name+" ("+t+")")
				} else {
					parts = append(parts, h.name)
				}
			}
			sb.WriteString("    params: " + strings.Join(parts, ", ") + "\n")
		}
		if w := op.Window; w != nil && w.Start != "" && w.End != "" {
			line := "    window: " + w.Start + " to " + w.End
			if w.Format != "" {
				line += " as " + w.Format
			}
			if w.Step != "" {
				line += ", step " + w.Step
			}
			sb.WriteString(line + "\n")
		}
	}
	if h := oneLine(e.ContextHint, 400); h != "" {
		sb.WriteString("  about this connection: " + h + "\n")
	}
}

// quoteText wraps a value in plain double quotes (no Go escaping).
func quoteText(s string) string { return `"` + s + `"` }
