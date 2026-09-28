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
	// ActorType is the server's own word for who wrote the row ("human",
	// "system", "agent"). Absent on older rows and on some fixtures, so a
	// reader must treat "" as "not known", never as "human" by itself.
	ActorType string `json:"actorType,omitempty"`

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
	// Basis is the server's word for what the item rests on. Optional.
	Basis string `json:"basis,omitempty"`
	// Admission is how an admitted claim got into the main context: the bar
	// was met by the corroborations listed, or an admin admitted it directly.
	// Absent on an older server and on anything not admitted.
	Admission *BriefAdmission `json:"admission,omitempty"`
}

// BriefAdmission is how one brief item was admitted. Names are display names,
// never ids.
type BriefAdmission struct {
	// Trigger is "bar-met" or "human-override".
	Trigger       string              `json:"trigger"`
	DecidedBy     string              `json:"decidedBy,omitempty"`
	Reason        string              `json:"reason,omitempty"`
	Corroborators []BriefCorroborator `json:"corroborators"`
}

// BriefCorroborator is one vote the admission gate counted.
type BriefCorroborator struct {
	By     string `json:"by"`
	Kind   string `json:"kind"`
	Reason string `json:"reason,omitempty"`
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
	// DisplayID is the incident's human id, "{organization name} {number}"
	// ("Acme 42"), the one the web app shows. Absent from a server that does
	// not send it; the title is the fallback wherever a person reads the name.
	DisplayID   string `json:"displayId,omitempty"`
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

// WidgetCatalogEntry is one canvas widget type, as the server describes it
// (monorepo feature 20260904-130050: the one widget catalog every surface
// derives from). Carried on the ContextFrame so `describe_widget_types` and
// `share_with_room`'s widget argument never need a separate round trip.
type WidgetCatalogEntry struct {
	Type      string   `json:"type"`
	Label     string   `json:"label"`
	Purpose   string   `json:"purpose"`
	BestFor   []string `json:"bestFor"`
	DataShape string   `json:"dataShape"`
}

// --- monorepo 20260921-101054-war-room-chat-context: the room's own scope ---
//
// A human can pin what an investigation is about (repos, architecture
// components, telemetry sources, a time window, artifacts, past incidents),
// write standing guidance for the organization, steer a run, and turn
// Beacon's chat replies off. All four ride on the SAME frame read this CLI
// already makes, so `get_brief` carries them with no extra round trip — the
// Landfall Edge desktop bridge picks the same four fields by name.
//
// Every field is optional on the wire and stays optional here: a server older
// than that feature simply omits them, and the renderer drops the block.
// There is deliberately no matching WRITE verb in this CLI: `context.attached`,
// `steer.recorded` and `beacon.listening.changed` are in the server's
// HUMAN_ONLY_EVENT_TYPES, so an agent principal cannot append them at all.
// Pinning scope is a person's decision about their own room.

// ActorRef is who did something, as the frame reports it. `DisplayName` is
// absent when the server could not resolve the member.
type ActorRef struct {
	HumanActorID string `json:"humanActorId"`
	DisplayName  string `json:"displayName"`
}

// FrameAttachment is one live pinned scope item — a `context.attached` row
// not since detached. `Label` is enriched by the server (the pure projection
// never looks anything up), so it is what to render; `Ref` carries the
// kind-specific ids for an agent that wants to act on them.
type FrameAttachment struct {
	Seq   int64             `json:"seq"`
	Kind  string            `json:"kind"`
	Ref   map[string]string `json:"ref"`
	Label string            `json:"label"`
	By    ActorRef          `json:"by"`
	Stale bool              `json:"stale"`
}

// FrameInstructions is the organization's standing guidance document.
// `ComponentSections` arrives ALREADY filtered by the server to the
// components pinned to this room, so rendering every entry is correct — this CLI must not re-filter it and
// must not assume an unpinned component's section is reachable here.
type FrameInstructions struct {
	Version           int64             `json:"version"`
	Body              string            `json:"body"`
	ComponentSections map[string]string `json:"componentSections"`
}

// FrameFocus is what a human most recently asked the investigation to focus
// on: the newest `steer.recorded` since the last run started.
type FrameFocus struct {
	Focus string   `json:"focus"`
	By    ActorRef `json:"by"`
}

// ContextFrame is GET /edge/context/frame.
type ContextFrame struct {
	AsOfSeq       *int64               `json:"asOfSeq"`
	Version       int64                `json:"version"`
	FreshnessMs   *float64             `json:"freshnessMs"`
	Incident      Incident             `json:"incident"`
	Brief         Brief                `json:"brief"`
	Participants  []Participant        `json:"participants"`
	WidgetCatalog []WidgetCatalogEntry `json:"widgetCatalog,omitempty"`
	Attachments   []FrameAttachment    `json:"attachments,omitempty"`
	Instructions  *FrameInstructions   `json:"instructions,omitempty"`
	// A pointer, not a bool: `listening` absent means a server that does not
	// know about the feature, which must not read as "Beacon is muted".
	Listening *bool       `json:"listening,omitempty"`
	Focus     *FrameFocus `json:"focus,omitempty"`
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

// --- 20260906-204144-data-source-sdk / landfall-cli#12: signal catalog + query ---
//
// Both types are kept as raw/opaque JSON rather than typed fields. A catalog
// entry's shape (kinds served, advertised operations) and a query result's
// shape (the provider's own response envelope) are server- and plugin-owned,
// not this client's — constitution v2.2.0's "raw stays on the wire, shaping
// is optional and plugin-owned" amendment applies here exactly as it does to
// the Edge control-plane client this mirrors.

// SignalCatalogEntry is one connected source's catalog entry, verbatim.
type SignalCatalogEntry = json.RawMessage

// SignalsQueryResult is the provider's raw response envelope from
// POST .../plugins/:source/invoke, verbatim.
type SignalsQueryResult = map[string]any

// ArtifactListItem is one row of GET /artifacts (monorepo
// `artifacts.controller.ts` list) — what the room shows in its artifact list,
// minus nothing an agent needs to pick one to read.
type ArtifactListItem struct {
	ArtifactID     string `json:"artifactId"`
	Filename       string `json:"filename"`
	ContentType    string `json:"contentType"`
	Size           int64  `json:"size"`
	SafeRenderMode string `json:"safeRenderMode"`
	SharedAt       string `json:"sharedAt"`
	Sharer         struct {
		DisplayName string `json:"displayName"`
		Kind        string `json:"kind"`
	} `json:"sharer"`
}

// ArtifactList is the answer to GET /artifacts.
type ArtifactList struct {
	Artifacts []ArtifactListItem `json:"artifacts"`
}
