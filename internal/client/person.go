package client

// person.go — the reads and writes a PERSON makes in a room, as opposed to
// their agent (landfall-cli edge components, review findings 1, 2, 5, 7).
//
// Every method here goes out WITHOUT an agentInstanceId, on purpose. The server
// resolves a request that names no instance to the session's human
// (`req.user.sub`), so:
//
//   - GetPersonAttention is the human view of what awaits a vote: the agent's
//     view drops a claim the agent has already positioned on and holds a
//     teammate's gate-held claim back as a bare count (`waitingForPeople`);
//   - PositionAsPerson records the position as the person, never as their
//     agent (`Client.PositionClaim` always names the instance);
//   - ClaimLine and ReleaseLine act as the person, which is the only actor
//     the server lets hold a line.
//
// Which token the client carries decides WHICH person: the caller builds a
// client with the person's own OAuth token, or with the room token only when
// that token's subject is the person (see internal/cli/vote.go).

import (
	"context"
	"net/http"
	"strconv"
)

// ClaimActor is who authored or positioned on a claim, as the claims
// projection names them. Display names are what to show; ids only match.
type ClaimActor struct {
	HumanActorID    string `json:"humanActorId"`
	AgentInstanceID string `json:"agentInstanceId,omitempty"`
	EdgeAgentLabel  string `json:"edgeAgentLabel,omitempty"`
	DisplayName     string `json:"displayName,omitempty"`
	Kind            string `json:"kind,omitempty"`
}

// ClaimProvenance is one reference a claim carries as its evidence.
type ClaimProvenance struct {
	SourceType string `json:"sourceType"`
	SourceSeq  *int64 `json:"sourceSeq,omitempty"`
	Quote      string `json:"quote"`
}

// ClaimPosition is one participant's active corroborate/contest.
type ClaimPosition struct {
	Position string     `json:"position"`
	Actor    ClaimActor `json:"actor"`
	At       string     `json:"at,omitempty"`
}

// ClaimBar is the bar a claim had to meet, as the outcome reports it.
type ClaimBar struct {
	Corroborators int `json:"corroborators"`
}

// ClaimOutcome is the admission outcome recomputed at projection time.
type ClaimOutcome struct {
	Admit       bool       `json:"admit"`
	Shortfall   *Shortfall `json:"shortfall,omitempty"`
	RequiredBar *ClaimBar  `json:"requiredBar,omitempty"`
}

// ClaimView is one claim of GET …/claims.
type ClaimView struct {
	Seq        int64             `json:"seq"`
	Class      string            `json:"class"`
	Author     ClaimActor        `json:"author"`
	Statement  string            `json:"statement"`
	Provenance []ClaimProvenance `json:"provenance"`
	// State is staged, admitted or withdrawn.
	State     string          `json:"state"`
	At        string          `json:"at"`
	Positions []ClaimPosition `json:"positions,omitempty"`
	Outcome   *ClaimOutcome   `json:"outcome,omitempty"`
	Stale     bool            `json:"stale,omitempty"`
}

// ClaimsProjection is GET /o/:slug/incidents/:id/claims.
type ClaimsProjection struct {
	Claims       []ClaimView `json:"claims"`
	AdmittedSeqs []int64     `json:"admittedSeqs"`
	GateEnabled  bool        `json:"gateEnabled"`
}

// LineHolder is who holds a line of investigation.
type LineHolder struct {
	HumanActorID string `json:"humanActorId"`
	DisplayName  string `json:"displayName"`
	Kind         string `json:"kind"`
}

// LineClaim is one entry of GET …/edge/lines (`LineClaimEntry`).
type LineClaim struct {
	ClaimID      string     `json:"claimId"`
	LineKey      string     `json:"lineKey"`
	Label        string     `json:"label"`
	Holder       LineHolder `json:"holder"`
	ClaimedAtSeq int64      `json:"claimedAtSeq"`
	// State is held, released or transferred.
	State         string `json:"state"`
	ReleasedAtSeq *int64 `json:"releasedAtSeq,omitempty"`
}

// LineClaimOutcome is the answer to POST …/edge/lines: `claimed`, or `lost`
// with the holder who already has the line (a normal outcome, not an error).
type LineClaimOutcome struct {
	Outcome string `json:"outcome"`
	LineKey string `json:"lineKey"`
	ClaimID string `json:"claimId,omitempty"`
	HeldBy  *struct {
		DisplayName string `json:"displayName"`
		Kind        string `json:"kind"`
	} `json:"heldBy,omitempty"`
	Seq int64 `json:"seq"`
}

// LineReleaseOutcome is the answer to DELETE …/edge/lines/:claimId:
// `released`, or `not-held` for a claim already released (quietly idempotent).
type LineReleaseOutcome struct {
	Outcome string `json:"outcome"`
	ClaimID string `json:"claimId"`
	Seq     int64  `json:"seq"`
}

// PositionResult is the answer to POST …/claims/:seq/position.
type PositionResult struct {
	OK    bool   `json:"ok"`
	State string `json:"state"`
}

// GetPersonAttention is GET …/vetting/attention with no agentInstanceId: what
// awaits the session's human, statements included (review finding 2).
func (c *Client) GetPersonAttention(ctx context.Context) (*Attention, error) {
	var att Attention
	if err := c.get(ctx, "/vetting/attention", &att); err != nil {
		return nil, err
	}
	return &att, nil
}

// GetClaims is GET …/claims: the staging area and the admitted set, with each
// claim's author, evidence, positions and outcome.
func (c *Client) GetClaims(ctx context.Context) (*ClaimsProjection, error) {
	var out ClaimsProjection
	if err := c.get(ctx, "/claims", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetLines is GET …/edge/lines: every line claim on the incident, held or not.
func (c *Client) GetLines(ctx context.Context) ([]LineClaim, error) {
	var out []LineClaim
	if err := c.get(ctx, "/edge/lines", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// PositionAsPerson records corroborate or contest on a staged claim as the
// session's human. Unlike PositionClaim, the body has no agentInstanceId key
// at all, so the server cannot read it as the agent's.
func (c *Client) PositionAsPerson(ctx context.Context, claimSeq int64, position, reason string) (*PositionResult, error) {
	body := map[string]any{"position": position}
	if reason != "" {
		body["reason"] = reason
	}
	var out PositionResult
	path := "/claims/" + encodeURIComponent(strconv.FormatInt(claimSeq, 10)) + "/position"
	if err := c.send(ctx, http.MethodPost, path, body, &out, true); err != nil {
		return nil, err
	}
	return &out, nil
}

// ClaimLine claims a line of investigation for the session's human. The body
// is the server's strict schema: {lineKey, label, idempotencyKey}, nothing else.
func (c *Client) ClaimLine(ctx context.Context, lineKey, label, idempotencyKey string) (*LineClaimOutcome, error) {
	var out LineClaimOutcome
	body := map[string]any{"lineKey": lineKey, "label": label, "idempotencyKey": idempotencyKey}
	if err := c.send(ctx, http.MethodPost, "/edge/lines", body, &out, true); err != nil {
		return nil, err
	}
	return &out, nil
}

// ReleaseLine hands a held line back. Only its holder may.
func (c *Client) ReleaseLine(ctx context.Context, claimID, idempotencyKey string) (*LineReleaseOutcome, error) {
	var out LineReleaseOutcome
	path := "/edge/lines/" + encodeURIComponent(claimID)
	if err := c.send(ctx, http.MethodDelete, path, map[string]any{"idempotencyKey": idempotencyKey}, &out, true); err != nil {
		return nil, err
	}
	return &out, nil
}
