// Package client is the Edge Bridge's outbound connection to Landfall — a Go
// port of `src/client.mjs`. It wraps the feature-006 `edge/*` HTTP contract
// with the teammate's incident-scoped session token. The transport is
// injectable (a `Doer`) so the client is testable without a network.
// Read-only by default; contributions/actions are propose-only + approval-
// gated server-side.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// Doer is the injectable transport. *http.Client satisfies it.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Config is the incident-scoped bridge configuration — what `redeemShareLink`
// returns, plus the label this agent narrates under.
type Config struct {
	BaseURL      string
	Slug         string
	IncidentID   string
	Token        string
	AgentLabel   string
	HumanActorID string
}

// Client is one joined (or not-yet-joined) edge session.
type Client struct {
	cfg  Config
	http Doer

	// The server-issued instance id is written by Join and read by every
	// background refresh, so it is mutex-guarded rather than a bare field.
	mu              sync.RWMutex
	agentInstanceID string
}

// New builds a client. A nil Doer falls back to http.DefaultClient.
func New(cfg Config, doer Doer) *Client {
	if doer == nil {
		doer = http.DefaultClient
	}
	return &Client{cfg: cfg, http: doer}
}

// Config returns the incident-scoped configuration this client speaks for.
func (c *Client) Config() Config { return c.cfg }

// AgentInstanceID is the id the SERVER issued at Join — empty before one has
// been issued. It is not an identity claim: the server checks it against this
// incident's own join grant and refuses one it never issued.
func (c *Client) AgentInstanceID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.agentInstanceID
}

// SetAgentInstanceID exists for tests and for a caller restoring a session.
func (c *Client) SetAgentInstanceID(id string) {
	c.mu.Lock()
	c.agentInstanceID = id
	c.mu.Unlock()
}

func (c *Client) base() string {
	return strings.TrimSuffix(c.cfg.BaseURL, "/") + "/o/" + c.cfg.Slug + "/incidents/" + c.cfg.IncidentID
}

// errorBody is the shape a rejection may carry: the server-provided `reason`
// (e.g. the artifact policy rejection) or `message`, surfaced so the caller
// can relay a clear cause and not just a status.
type errorBody struct {
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

// post issues a POST and decodes into `out` (which may be nil). A 202 carries
// no body, exactly as the Node client assumed.
func (c *Client) post(ctx context.Context, path string, body map[string]any, out any) error {
	if body == nil {
		body = map[string]any{}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("%s → %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base()+path, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("%s → %w", path, err)
	}
	req.Header.Set("authorization", "Bearer "+c.cfg.Token)
	req.Header.Set("content-type", "application/json")

	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s → %w", path, err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode < 200 || res.StatusCode > 299 {
		reason := ""
		var eb errorBody
		if raw, readErr := io.ReadAll(res.Body); readErr == nil {
			if json.Unmarshal(raw, &eb) == nil {
				reason = eb.Reason
				if reason == "" {
					reason = eb.Message
				}
			}
		}
		if reason != "" {
			return fmt.Errorf("%s → HTTP %d: %s", path, res.StatusCode, reason)
		}
		return fmt.Errorf("%s → HTTP %d", path, res.StatusCode)
	}
	if res.StatusCode == http.StatusAccepted || out == nil {
		_, _ = io.Copy(io.Discard, res.Body)
		return nil
	}
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("%s → %w", path, err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s → %w", path, err)
	}
	return nil
}

// get issues a GET and decodes into `out`.
func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base()+path, nil)
	if err != nil {
		return fmt.Errorf("%s → %w", path, err)
	}
	req.Header.Set("authorization", "Bearer "+c.cfg.Token)

	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s → %w", path, err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode < 200 || res.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, res.Body)
		return fmt.Errorf("%s → HTTP %d", path, res.StatusCode)
	}
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("%s → %w", path, err)
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s → %w", path, err)
	}
	return nil
}

// identity is the {agentInstanceId, edgeAgentLabel} pair that rides along on
// the vetting/claims POSTs. Neither field asserts an identity of its own: the
// server resolves the human and the display name itself.
//
// A JS `undefined` is dropped by JSON.stringify while a JS `null` is emitted,
// so the two are mirrored exactly: `agentInstanceId` is always present (null
// before Join), `edgeAgentLabel` is omitted when unset.
func (c *Client) identity(body map[string]any) map[string]any {
	if id := c.AgentInstanceID(); id != "" {
		body["agentInstanceId"] = id
	} else {
		body["agentInstanceId"] = nil
	}
	if c.cfg.AgentLabel != "" {
		body["edgeAgentLabel"] = c.cfg.AgentLabel
	}
	return body
}

// Join joins the incident with this member's edge agent; the server issues an
// instance id, which is stored for every later call.
func (c *Client) Join(ctx context.Context) (*JoinResult, error) {
	label := c.cfg.AgentLabel
	if label == "" {
		label = "edge-agent"
	}
	var res JoinResult
	if err := c.post(ctx, "/edge/join", map[string]any{"edgeAgentLabel": label}, &res); err != nil {
		return nil, err
	}
	c.SetAgentInstanceID(res.AgentInstanceID)
	return &res, nil
}

// Heartbeat reports liveness + current activity → presence + the "who's doing
// what" summary.
func (c *Client) Heartbeat(ctx context.Context, doing string) error {
	body := map[string]any{"doing": doing}
	if id := c.AgentInstanceID(); id != "" {
		body["agentInstanceId"] = id
	} else {
		body["agentInstanceId"] = nil
	}
	return c.post(ctx, "/edge/heartbeat", body, nil)
}

// Contribute consolidates a contribution (finding | query | hypothesis |
// action | widget) onto the shared timeline. `body`'s keys are spread over the
// envelope, matching the Node spread order (body wins on a key collision).
func (c *Client) Contribute(ctx context.Context, kind string, body map[string]any) error {
	payload := map[string]any{"kind": kind}
	if id := c.AgentInstanceID(); id != "" {
		payload["agentInstanceId"] = id
	} else {
		payload["agentInstanceId"] = nil
	}
	for k, v := range body {
		payload[k] = v
	}
	return c.post(ctx, "/edge/contributions", payload, nil)
}

// Leave releases this agent's seat.
func (c *Client) Leave(ctx context.Context) error {
	body := map[string]any{}
	if id := c.AgentInstanceID(); id != "" {
		body["agentInstanceId"] = id
	} else {
		body["agentInstanceId"] = nil
	}
	return c.post(ctx, "/edge/leave", body, nil)
}

// UploadArtifact shares a locally-created artifact into the incident (feature
// 025). Bytes are base64. The server enforces the size/type policy, stores the
// bytes, and appends the durable `artifact.shared` event — this is NOT an
// execution channel.
func (c *Client) UploadArtifact(ctx context.Context, filename, contentType, dataBase64 string) (*ArtifactResult, error) {
	body := map[string]any{
		"filename":    filename,
		"contentType": contentType,
		"dataBase64":  dataBase64,
	}
	if c.cfg.AgentLabel != "" {
		body["edgeAgentLabel"] = c.cfg.AgentLabel
	}
	if id := c.AgentInstanceID(); id != "" {
		body["agentInstanceId"] = id
	} else {
		body["agentInstanceId"] = nil
	}
	var res ArtifactResult
	if err := c.post(ctx, "/artifacts", body, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// FlagContext flags a published context item (chat message or finding) as
// wrong or misleading (feature 029). This is a POSITION, not a decision: the
// server tallies distinct actors and requires a human among the concurring
// voters before anything is quarantined.
func (c *Client) FlagContext(ctx context.Context, targetSeq int64, reason, targetKind string) error {
	body := c.identity(map[string]any{
		"targetSeq": targetSeq,
		"reason":    reason,
	})
	if targetKind != "" {
		body["targetKind"] = targetKind
	}
	return c.post(ctx, "/vetting/flag", body, nil)
}

// PositionClaim takes a position on a STAGED claim (feature 034):
// `corroborate` or `contest`. One active position per participant — repeating
// it replaces, never adds.
func (c *Client) PositionClaim(ctx context.Context, claimSeq int64, position, reason string) error {
	body := c.identity(map[string]any{"position": position})
	if reason != "" {
		body["reason"] = reason
	}
	path := "/claims/" + encodeURIComponent(strconv.FormatInt(claimSeq, 10)) + "/position"
	return c.post(ctx, path, body, nil)
}

// StageClaim stages a claim (feature 034). It enters the staging area — NOT
// the room feed and NOT any other participant's agent context — until it earns
// admission.
func (c *Client) StageClaim(ctx context.Context, body map[string]any) error {
	payload := c.identity(map[string]any{})
	for k, v := range body {
		payload[k] = v
	}
	return c.post(ctx, "/claims", payload, nil)
}

// GetBrief returns the incident context for the local agent: the current
// timeline (read-only).
func (c *Client) GetBrief(ctx context.Context) ([]Event, error) {
	var events []Event
	if err := c.get(ctx, "/events", &events); err != nil {
		return nil, err
	}
	return events, nil
}

// GetUpdates is the durable-cursor delta read (021): only events with
// seq > sinceSeq.
func (c *Client) GetUpdates(ctx context.Context, sinceSeq int64) ([]Event, error) {
	var events []Event
	path := "/events?sinceSeq=" + encodeURIComponent(strconv.FormatInt(sinceSeq, 10))
	if err := c.get(ctx, path, &events); err != nil {
		return nil, err
	}
	return events, nil
}

// GetContextFrame returns the war-room context frame (feature 116).
func (c *Client) GetContextFrame(ctx context.Context) (*ContextFrame, error) {
	var frame ContextFrame
	if err := c.get(ctx, "/edge/context/frame", &frame); err != nil {
		return nil, err
	}
	return &frame, nil
}

// GetContextDelta returns what changed since `sinceVersion` (feature 116),
// already classified by the server. `agentInstanceId` is what lets the server
// tell this agent's OWN activity apart from everyone else's.
func (c *Client) GetContextDelta(ctx context.Context, sinceVersion int64) (*FrameDelta, error) {
	path := "/edge/context/delta?sinceVersion=" + encodeURIComponent(strconv.FormatInt(sinceVersion, 10))
	if id := c.AgentInstanceID(); id != "" {
		path += "&agentInstanceId=" + encodeURIComponent(id)
	}
	var delta FrameDelta
	if err := c.get(ctx, path, &delta); err != nil {
		return nil, err
	}
	return &delta, nil
}

// SearchContext returns real search hits (feature 116) — seq/type/author/
// snippet, never a bare count.
func (c *Client) SearchContext(ctx context.Context, query string) (*SearchResult, error) {
	path := "/edge/context/search?q=" + encodeURIComponent(query)
	if id := c.AgentInstanceID(); id != "" {
		path += "&agentInstanceId=" + encodeURIComponent(id)
	}
	var res SearchResult
	if err := c.get(ctx, path, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// GetAttention returns what the room is waiting on from THIS agent (#252).
// The parameter is omitted entirely before the server has issued an instance:
// sending an empty one would be a claim about identity the server must then
// refuse, while sending none is the honest "ask as the human" it supports.
func (c *Client) GetAttention(ctx context.Context) (*Attention, error) {
	path := "/vetting/attention"
	if id := c.AgentInstanceID(); id != "" {
		path += "?agentInstanceId=" + encodeURIComponent(id)
	}
	var att Attention
	if err := c.get(ctx, path, &att); err != nil {
		return nil, err
	}
	return &att, nil
}

// GetDivergence asks whether THIS agent's recent read history is drifting from
// the room's established causal claim. Same `agentInstanceId` discipline as
// GetAttention; the server recomputes it fresh on every call.
func (c *Client) GetDivergence(ctx context.Context) (*Divergence, error) {
	path := "/vetting/divergence"
	if id := c.AgentInstanceID(); id != "" {
		path += "?agentInstanceId=" + encodeURIComponent(id)
	}
	var d Divergence
	if err := c.get(ctx, path, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// GetSignalCatalog lists the telemetry sources connected for this incident's
// organization (20260906-204144-data-source-sdk / landfall-cli#12): each
// source, the kinds of signal it serves, and the read operations it
// advertises. Mirrors the Edge control-plane client's `getSignalCatalog`
// exactly, including its tolerance: the route may answer with a bare array
// or with `{plugins: [...]}}`, and either is accepted — an unrecognized
// shape (neither) degrades to an empty catalog rather than an error, same as
// the JS original's `Array.isArray(...) ? ... : []`.
func (c *Client) GetSignalCatalog(ctx context.Context) ([]SignalCatalogEntry, error) {
	path := "/plugins"
	if id := c.AgentInstanceID(); id != "" {
		path += "?agentInstanceId=" + encodeURIComponent(id)
	}
	var raw json.RawMessage
	if err := c.get(ctx, path, &raw); err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return []SignalCatalogEntry{}, nil
	}
	var asArray []SignalCatalogEntry
	if err := json.Unmarshal(raw, &asArray); err == nil {
		return asArray, nil
	}
	var wrapped struct {
		Plugins []SignalCatalogEntry `json:"plugins"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil || wrapped.Plugins == nil {
		return []SignalCatalogEntry{}, nil
	}
	return wrapped.Plugins, nil
}

// QuerySignals reads one operation from one connected source through
// Landfall's credential proxy — the provider's raw response envelope,
// verbatim (constitution v2.2.0: raw stays on the wire). `connectionID`/
// `accountID` are included in the request body only when non-empty, and
// `agentInstanceId` only once Join has issued one — all three omitted rather
// than sent empty/null, matching the Edge control-plane client's
// `querySignals` body exactly (unlike this package's `identity()` helper,
// which the JS original does NOT use for this route).
func (c *Client) QuerySignals(ctx context.Context, source, operation string, params map[string]any, connectionID, accountID string) (SignalsQueryResult, error) {
	if params == nil {
		params = map[string]any{}
	}
	body := map[string]any{
		"operation": operation,
		"params":    params,
	}
	if connectionID != "" {
		body["connectionId"] = connectionID
	}
	if accountID != "" {
		body["accountId"] = accountID
	}
	if id := c.AgentInstanceID(); id != "" {
		body["agentInstanceId"] = id
	}
	var result SignalsQueryResult
	path := "/plugins/" + encodeURIComponent(source) + "/invoke"
	if err := c.post(ctx, path, body, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// encodeURIComponent mirrors JS's encodeURIComponent for the characters that
// matter here: Go's url.QueryEscape renders a space as `+`, which the JS
// original never does.
func encodeURIComponent(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}
