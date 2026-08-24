package bridge

import (
	"context"
	"fmt"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

// THE WORKER DOES NOT VOTE. Read this before "finishing" FR-006.
//
// spec FR-006 says the worker "MUST be able to participate in vetting (stage /
// corroborate / contest / flag) for content it published, without routing vote
// requests through the main agent". Implemented literally, that is either a
// no-op or unsafe, and the source says so in two places:
//
//  1. Corroborating your own claim is ALREADY excluded server-side — the
//     corroborate_claim tool's own description states admission "never counts
//     the claim author corroborating themselves". So a worker voting on
//     "content it published" changes nothing except the appearance of support.
//
//  2. Voting on ANYONE ELSE'S claim requires weighing evidence. This worker has
//     no model and spec D5 keeps it that way. A vote it cast would be a
//     position recorded under the responder's identity, in a room where
//     outcomes are computed from distinct participants, based on no judgement
//     at all. That is worse than not voting: it manufactures consensus.
//
// The room's own standing instructions say it plainly — "vote from evidence and
// then move on rather than arguing for your own claim". A model-free background
// process has no evidence-weighing to do.
//
// So this file implements the MECHANICAL half and refuses the judgement half:
//
//	staging     YES — the responder explicitly wrote "I claim X". Staging is
//	            transcription, not evaluation.
//	surfacing   YES — vote requests and flags on the responder's content are
//	            `addressed` class, so under D8 they reach the main agent in band
//	            and a human or their agent decides.
//	voting      NO  — see above. Deliberately absent, not unimplemented.
//
// If autonomous voting is ever wanted, it needs an explicit operator decision
// with its own safety argument, not a quiet extension of this file.

// stageableClaim reports whether a hand-off should be staged for the room to
// vet rather than published as a plain finding.
//
// The bar is the responder's own explicit framing (Classify's KindClaim), for
// the same reason the classifier is conservative: staging invites other
// investigators to spend attention voting, so inferring a claim from a hedge
// would tax the room during an incident.
func stageableClaim(kind Kind) bool { return kind == KindClaim }

// surfaceAttention reports vetting work the responder owes the room.
//
// It writes to stderr and stops there. It does NOT vote, and it does not try to
// answer on the responder's behalf — the request itself is `addressed` class,
// so D8 already delivers it to the main agent in band, where a human or their
// agent can weigh it. This line exists so an operator watching the terminal
// sees the same thing, not to create a second decision path.
func (w *Worker) surfaceAttention(ctx context.Context, pub Publisher) {
	att, ok := attentionOf(ctx, pub)
	if !ok || att == nil {
		return
	}

	if n := len(att.VotesAwaited); n > 0 {
		w.log("bridge: %s awaiting your position — answer in the room when you have evidence; the bridge will not vote for you", plural(n, "claim"))
	}
	if n := len(att.FlaggedOwnContext); n > 0 {
		w.log("bridge: %s of yours %s been flagged as wrong — worth a look", plural(n, "item"), wereOrWas(n))
	}
}

// attentionOf reads the attention projection when the publisher can provide
// one. Optional by design: the worker's core job (queue in, publish out) must
// not depend on a vetting read succeeding.
func attentionOf(ctx context.Context, pub Publisher) (*client.Attention, bool) {
	type attentionReader interface {
		GetAttention(ctx context.Context) (*client.Attention, error)
	}
	r, ok := pub.(attentionReader)
	if !ok {
		return nil, false
	}
	att, err := r.GetAttention(ctx)
	if err != nil {
		// Best-effort: a room we cannot reach is not an error the responder
		// needs surfaced mid-investigation.
		return nil, false
	}
	return att, true
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func wereOrWas(n int) string {
	if n == 1 {
		return "has"
	}
	return "have"
}
