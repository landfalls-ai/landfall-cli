package client

import "encoding/json"

// The wire types of the edge/* HTTP contract. These are decoded from the
// server's JSON exactly as the Node client received it — pointers where the
// distinction between "absent" and "zero" is load-bearing (a seq of 0 is a
// real seq; a claim of 0 is a real claim).

// Event is one durable timeline event. `Seq` is a pointer because
// `enqueueEvent` requires a NUMERIC seq — an event without one is neither
// dedupable nor comparable against the cursor, and must be refused rather
// than treated as seq 0.
//
// `Raw` keeps the bytes the server actually sent, so `read_timeline` can hand
// the agent the whole event verbatim (the Node original returns
// `JSON.stringify(events)` over the parsed wire payload, unknown fields and
// all) rather than a lossy re-render of the fields this struct happens to name.
type Event struct {
	Seq     *int64         `json:"seq"`
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload,omitempty"`

	Raw json.RawMessage `json:"-"`
}

// UnmarshalJSON decodes the named fields and keeps the original bytes.
func (e *Event) UnmarshalJSON(b []byte) error {
	type alias Event
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*e = Event(a)
	e.Raw = append(json.RawMessage(nil), b...)
	return nil
}

// MarshalJSON re-emits the original bytes when there are any.
func (e Event) MarshalJSON() ([]byte, error) {
	if len(e.Raw) > 0 {
		return e.Raw, nil
	}
	type alias Event
	return json.Marshal(alias(e))
}

// SeqOr returns the event's seq, or `fallback` when it carries none.
func (e Event) SeqOr(fallback int64) int64 {
	if e.Seq == nil {
		return fallback
	}
	return *e.Seq
}

// JoinResult is the answer to POST /edge/join.
type JoinResult struct {
	AgentInstanceID string `json:"agentInstanceId"`
}

// ArtifactResult is the answer to POST /artifacts.
type ArtifactResult struct {
	ArtifactID     string `json:"artifactId"`
	Filename       string `json:"filename"`
	ContentType    string `json:"contentType"`
	Size           int64  `json:"size"`
	SafeRenderMode string `json:"safeRenderMode"`
}

// --- feature 116: the war-room context frame/delta/search reads -------------

// BriefItem is one line of the established/working-theory/open brief.
type BriefItem struct {
	Seq       int64  `json:"seq"`
	Statement string `json:"statement"`
	By        string `json:"by"`
}

// Brief is the established-vs-open summary carried by a ContextFrame.
type Brief struct {
	Established   []BriefItem `json:"established"`
	WorkingTheory []BriefItem `json:"workingTheory"`
	Disproved     []BriefItem `json:"disproved"`
	Open          []BriefItem `json:"open"`
}

// Incident is the frame's incident header.
type Incident struct {
	Title       string `json:"title"`
	Severity    string `json:"severity"`
	Status      string `json:"status"`
	AlertSource string `json:"alertSource"`
}

// Participant is one member of the room, with a three-state presence: `Active`
// is a pointer because `undefined` on the wire means the server's presence
// store could not be read — "unknown", never fabricated as active or away.
type Participant struct {
	DisplayName    string `json:"displayName"`
	EdgeAgentLabel string `json:"edgeAgentLabel"`
	Active         *bool  `json:"active"`
}

// ContextFrame is GET /edge/context/frame.
type ContextFrame struct {
	AsOfSeq      *int64        `json:"asOfSeq"`
	Version      int64         `json:"version"`
	FreshnessMs  *float64      `json:"freshnessMs"`
	Incident     Incident      `json:"incident"`
	Brief        Brief         `json:"brief"`
	Participants []Participant `json:"participants"`
}

// DeltaItem is one classified change in a FrameDelta.
type DeltaItem struct {
	Seq     int64  `json:"seq"`
	Type    string `json:"type"`
	By      string `json:"by"`
	Class   string `json:"class"`
	Summary string `json:"summary"`
}

// FrameDelta is GET /edge/context/delta — the server's own classification of
// what changed, which is what both `get_updates` and the piggyback flush
// render. The CLI never re-derives this ranking locally.
type FrameDelta struct {
	SinceVersion int64       `json:"sinceVersion"`
	ToVersion    *int64      `json:"toVersion"`
	Items        []DeltaItem `json:"items"`
	RoutineCount int         `json:"routineCount"`
}

// SearchHit is one real match from GET /edge/context/search.
type SearchHit struct {
	Seq     int64  `json:"seq"`
	Type    string `json:"type"`
	By      string `json:"by"`
	Snippet string `json:"snippet"`
}

// SearchResult is GET /edge/context/search.
type SearchResult struct {
	Hits []SearchHit `json:"hits"`
}

// --- #252 / feature 034: what the room is waiting on from THIS agent --------

// Missing names the specific shortfalls blocking a staged claim's admission.
type Missing struct {
	Contradiction []int64 `json:"contradiction"`
}

// Shortfall is why a staged claim has not been admitted yet.
type Shortfall struct {
	Text    string   `json:"text"`
	Missing *Missing `json:"missing"`
}

// VoteAwaited is a staged claim awaiting this agent's position.
type VoteAwaited struct {
	ClaimSeq       *int64     `json:"claimSeq"`
	Class          string     `json:"class"`
	Statement      string     `json:"statement"`
	AuthoredBy     string     `json:"authoredBy"`
	AuthorIsAgent  bool       `json:"authorIsAgent"`
	PositionsSoFar int        `json:"positionsSoFar"`
	Stale          bool       `json:"stale"`
	ExpiresInMs    *float64   `json:"expiresInMs"`
	Shortfall      *Shortfall `json:"shortfall"`
}

// FlaggedContext is context this agent authored or cited that has since been
// flagged or quarantined.
type FlaggedContext struct {
	TargetSeq        *int64  `json:"targetSeq"`
	TargetKind       string  `json:"targetKind"`
	State            string  `json:"state"`
	Relation         string  `json:"relation"`
	CitedByClaimSeqs []int64 `json:"citedByClaimSeqs"`
	Reason           string  `json:"reason"`
	Concur           int     `json:"concur"`
	Dissent          int     `json:"dissent"`
}

// Attention is GET /vetting/attention — the server-owned projection of what
// the room is waiting on from this agent instance.
type Attention struct {
	VotesAwaited      []VoteAwaited    `json:"votesAwaited"`
	FlaggedOwnContext []FlaggedContext `json:"flaggedOwnContext"`
	ExpiringClaims    []map[string]any `json:"expiringClaims"`
}

// Divergence is GET /vetting/divergence — is this agent's recent read history
// drifting from the room's established causal claim?
type Divergence struct {
	Diverging          bool   `json:"diverging"`
	EstablishedSubject string `json:"establishedSubject"`
	ObservedSubject    string `json:"observedSubject"`
}
