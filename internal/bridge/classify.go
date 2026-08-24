package bridge

import "strings"

// Kind is what a hand-off gets published as.
type Kind string

const (
	KindFinding Kind = "finding"
	KindNote    Kind = "note"
	KindWidget  Kind = "widget"
	KindClaim   Kind = "claim"
)

// Classify decides how to publish a free-text hand-off.
//
// DETERMINISTIC AND MODEL-FREE (spec D5). There is no model anywhere in this
// CLI and this is not the place to introduce one: the write path applies
// redaction locally before anything leaves the machine, so shipping hand-off
// text to a classifier would invert that ordering. Rules are also auditable —
// a responder can be told exactly why something was filed as it was.
//
// IT CAN NEVER RETURN AN ACTION, and that is a safety property rather than a
// missing feature (spec D2). A remediation proposal enters the human approval
// workflow. If a background worker could infer "this text is a remediation" and
// file one, an approval-gated act would have been initiated by an inference
// rather than by a person. propose_action stays an explicit main-agent tool for
// exactly this reason; there is no Kind for it here, so the type system rules
// it out rather than a code review having to.
//
// AMBIGUOUS INPUT BECOMES A NOTE. A note is the weakest claim available: it
// records what was said without asserting it as an established finding or
// staking a position the room might vote on. Guessing UP from ambiguity would
// put words in the responder's mouth.
func Classify(text string) Kind {
	t := strings.ToLower(strings.TrimSpace(text))
	if t == "" {
		return KindNote
	}

	// A claim is something the responder is explicitly offering for the room to
	// vet. Only an unambiguous framing counts — staging a claim invites other
	// investigators to spend effort voting, so inferring one from a passing
	// remark would waste the room's attention.
	for _, marker := range claimMarkers {
		if strings.Contains(t, marker) {
			return KindClaim
		}
	}

	// A widget is data the room can plot. Requires an explicit request: the
	// hand-off carries free text, not the structured values a widget needs, so
	// anything less than an explicit ask would produce an empty chart.
	for _, marker := range widgetMarkers {
		if strings.Contains(t, marker) {
			return KindWidget
		}
	}

	// A finding asserts something about the incident — the default for
	// substantive text, because that is what a responder handing something off
	// mid-investigation almost always means.
	if looksSubstantive(t) {
		return KindFinding
	}
	return KindNote
}

var claimMarkers = []string{
	"i claim",
	"claim:",
	"root cause is",
	"the root cause",
	"i'm confident",
	"i am confident",
}

var widgetMarkers = []string{
	"chart:",
	"widget:",
	"graph:",
	"plot:",
	"table:",
}

// looksSubstantive distinguishes an assertion about the incident from an aside.
//
// Deliberately shallow. A more clever heuristic would be harder to predict and
// harder to explain, and the cost of being wrong is small in one direction
// (a finding filed as a note is still in the room, verbatim) and larger in the
// other (a passing thought filed as a finding clutters the timeline other
// investigators are reading under pressure). So it errs toward note.
func looksSubstantive(t string) bool {
	if len(t) < 24 {
		return false
	}
	for _, marker := range findingMarkers {
		if strings.Contains(t, marker) {
			return true
		}
	}
	return false
}

var findingMarkers = []string{
	"error", "fail", "timeout", "timed out", "latency", "spike",
	"5xx", "4xx", "50", "regress", "leak", "deadlock", "throttl",
	"exception", "panic", "crash", "restart", "rollback", "degraded",
	"exhausted", "saturat", "retry", "retries", "unreachable", "refused",
	"correlat", "started at", "since ", "began ", "caused",
}
