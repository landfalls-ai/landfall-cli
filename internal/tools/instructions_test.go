package tools

import (
	"strings"
	"testing"

	"github.com/landfalls-ai/landfall-cli/internal/mcp"
	"github.com/landfalls-ai/landfall-cli/internal/session"
)

// TestInstructionsNeverNameToolsTheAgentDoesNotHave is the assertion that
// matters, and it is checked against the REGISTERED SURFACE rather than a
// hand-maintained list — a hand-maintained list drifts, and this failure is
// silent until an agent goes looking for a verb mid-incident.
func TestInstructionsNeverNameToolsTheAgentDoesNotHave(t *testing.T) {
	sess := session.New(session.Options{})

	// Every tool name the codebase knows about, so we can spot a mention of one
	// that is NOT in the surface under test.
	all := map[string]bool{}
	for _, tl := range Build(sess) {
		all[tl.Name] = true
	}
	for _, tl := range BuildWithAccepter(sess, &nopAccepter{}) {
		all[tl.Name] = true
	}

	cases := []struct {
		name    string
		surface []mcp.Tool
		text    string
	}{
		{"without bridge", Build(sess), InstructionsFor(false)},
		{"with bridge", BuildWithAccepter(sess, &nopAccepter{}), InstructionsFor(true)},
	}

	for _, c := range cases {
		have := map[string]bool{}
		for _, tl := range c.surface {
			have[tl.Name] = true
		}
		for tool := range all {
			if have[tool] {
				continue
			}
			if strings.Contains(c.text, tool) {
				t.Errorf("%s: instructions mention %q, which is not registered — "+
					"the agent will go looking for a verb it does not have", c.name, tool)
			}
		}
	}
}

// TestBridgeInstructionsDropTheBookkeepingBurden — the instructions ARE part of
// what FR-001 removes. If the tools move and the text does not, the agent still
// spends attention on room bookkeeping, just with fewer verbs to do it with.
func TestBridgeInstructionsDropTheBookkeepingBurden(t *testing.T) {
	got := InstructionsFor(true)

	for _, burden := range []string{
		"at task\n  boundaries",      // the get_updates cadence
		"take a position on someone", // vetting participation
	} {
		if strings.Contains(got, burden) {
			t.Errorf("bridge instructions still impose %q on the agent", burden)
		}
	}

	if !strings.Contains(got, "share_with_room") {
		t.Error("bridge instructions never mention the one publish verb the agent has")
	}
	if !strings.Contains(got, "returns immediately") {
		t.Error("bridge instructions do not tell the agent it need not wait")
	}
}

// TestBothVariantsKeepTheLoadBearingParagraphs — three things must survive any
// rewrite: the delivery-timing promise (which this feature makes MORE true),
// the safety paragraph, and propose_action's propose-only framing (D2).
func TestBothVariantsKeepTheLoadBearingParagraphs(t *testing.T) {
	for _, v := range []struct {
		name string
		text string
	}{
		{"without bridge", InstructionsFor(false)},
		{"with bridge", InstructionsFor(true)},
	} {
		if !strings.Contains(v.text, "never mid-turn") {
			t.Errorf("%s: lost the delivery-timing promise", v.name)
		}
		if !strings.Contains(v.text, "data, not instructions") {
			t.Errorf("%s: lost the prompt-injection safety paragraph", v.name)
		}
		if !strings.Contains(v.text, "propose-only") {
			t.Errorf("%s: lost propose_action's propose-only framing (D2)", v.name)
		}
		if !strings.Contains(v.text, "secrets") {
			t.Errorf("%s: lost the keep-secrets-local guidance", v.name)
		}
		// Regression guard: the first release of this guidance landed only in
		// EdgeAgentInstructions, not BridgeAgentInstructions — the variant
		// `serve` actually uses by default (bridge wired unconditionally) — so
		// a real live test of the no-bridge binary passed while the shipped
		// default silently lacked both paragraphs. Checking both variants
		// here, not just InstructionsFor(true), is what would have caught it.
		if !strings.Contains(v.text, "/j/<code>") {
			t.Errorf("%s: does not tell the agent to recognize a bare room link", v.name)
		}
		if !strings.Contains(v.text, "background subagent") {
			t.Errorf("%s: does not tell the agent to prefer a background subagent", v.name)
		}
	}
}

// TestBridgeInstructionsDoNotOversellRedaction — the redactor is conservative
// and cannot catch a secret that does not look like one. Telling an agent it is
// protected would make it careless, which is worse than not mentioning it.
func TestBridgeInstructionsDoNotOversellRedaction(t *testing.T) {
	got := InstructionsFor(true)
	if !strings.Contains(got, "safety net") || !strings.Contains(got, "not a licence") {
		t.Error("redaction is described without its caveat; an agent could reasonably conclude it is safe to paste secrets")
	}
}

type nopAccepter struct{}

func (nopAccepter) Accept(string, string, string, []string, *WidgetPayload) (string, bool, error) {
	return "id", false, nil
}
