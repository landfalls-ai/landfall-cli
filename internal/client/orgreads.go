package client

// orgreads.go — the reads the edge-component commands (`landfall incidents`,
// `join --incident`, `wall`, `brain --q`) make as THE PERSON, with their own
// `landfall login` token, against routes an incident-scoped edge session
// cannot reach:
//
//   - GET  /o/:slug/incidents              (an edge token is refused: it is
//     valid only on routes bound to its one incident, session-auth.guard.ts)
//   - POST /o/:slug/incidents/:id/edge/share-link
//   - POST /o/:slug/incidents/:id/widgets/data (per-viewer resolution keys on
//     the caller's own `sub`; a room session's `sub` is whoever minted the
//     share link, so the wall is read with the person's token instead)
//   - GET  /o/:slug/memory/entries
//
// Read-only except the share-link mint, which only issues this person a
// ticket to a room they may already open.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// OrgReader speaks for the signed-in person in one organization.
type OrgReader struct {
	BaseURL string
	Slug    string
	Token   string
	http    Doer
}

// NewOrgReader builds a reader. A nil Doer falls back to the client's bounded
// default transport.
func NewOrgReader(baseURL, slug, token string, doer Doer) *OrgReader {
	if doer == nil {
		doer = defaultHTTP
	}
	return &OrgReader{BaseURL: strings.TrimSuffix(baseURL, "/"), Slug: slug, Token: token, http: doer}
}

func (o *OrgReader) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("%s → %w", path, err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, o.BaseURL+"/o/"+o.Slug+path, reader)
	if err != nil {
		return fmt.Errorf("%s → %w", path, err)
	}
	req.Header.Set("authorization", "Bearer "+o.Token)
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	res, err := o.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s → %w", path, err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode < 200 || res.StatusCode > 299 {
		var eb errorBody
		reason := ""
		if json.Unmarshal(raw, &eb) == nil {
			reason = eb.text()
		}
		return &HTTPError{Path: path, Status: res.StatusCode, Reason: reason}
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s → %w", path, err)
	}
	return nil
}

// IncidentSummary is one row of GET /o/:slug/incidents (core-api
// `IncidentView`), the fields a switchboard draws.
type IncidentSummary struct {
	ID        string  `json:"id"`
	CreatedAt string  `json:"createdAt"`
	Number    int64   `json:"number"`
	DisplayID string  `json:"displayId"`
	Title     *string `json:"title"`
	Status    *string `json:"status"`
	Severity  *string `json:"severity"`
	Synthetic bool    `json:"synthetic,omitempty"`
}

// IncidentPage is one keyset page, newest number first.
type IncidentPage struct {
	Items      []IncidentSummary `json:"items"`
	NextCursor *string           `json:"nextCursor"`
}

// ListIncidents reads one page of the organization's incidents. limit is
// clamped to the route's 1..200.
func (o *OrgReader) ListIncidents(ctx context.Context, limit int, cursor string) (*IncidentPage, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 200 {
		limit = 200
	}
	path := "/incidents?limit=" + strconv.Itoa(limit)
	if cursor != "" {
		path += "&cursor=" + encodeURIComponent(cursor)
	}
	var page IncidentPage
	if err := o.do(ctx, http.MethodGet, path, nil, &page); err != nil {
		return nil, err
	}
	return &page, nil
}

// ShareLinkMint is POST …/edge/share-link's answer.
type ShareLinkMint struct {
	ShareURL  string `json:"shareUrl"`
	ExpiresAt string `json:"expiresAt,omitempty"`
}

// MintShareLink issues the person a single-use join link to one incident,
// exactly the link the web app's share dialog copies.
func (o *OrgReader) MintShareLink(ctx context.Context, incidentID string) (*ShareLinkMint, error) {
	var out ShareLinkMint
	path := "/incidents/" + encodeURIComponent(incidentID) + "/edge/share-link"
	if err := o.do(ctx, http.MethodPost, path, map[string]any{}, &out); err != nil {
		return nil, err
	}
	if out.ShareURL == "" {
		return nil, fmt.Errorf("%s → the answer carried no link", path)
	}
	return &out, nil
}

// WidgetsData resolves many widgets in one call, as the war-room canvas does,
// through the credential wall as this person. The body is returned verbatim.
func (o *OrgReader) WidgetsData(ctx context.Context, incidentID string, widgetIDs []string) (json.RawMessage, error) {
	var out json.RawMessage
	path := "/incidents/" + encodeURIComponent(incidentID) + "/widgets/data"
	if err := o.do(ctx, http.MethodPost, path, map[string]any{"widgetIds": widgetIDs}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// MemoryEntries reads every visible company second brain entry, verbatim.
func (o *OrgReader) MemoryEntries(ctx context.Context) (json.RawMessage, error) {
	var out json.RawMessage
	if err := o.do(ctx, http.MethodGet, "/memory/entries", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ReadJSON makes one read on this room's own session (its edge token, bound
// to its one incident) and returns the body verbatim. It sends no
// agentInstanceId: these reads are the person's commands, never an agent
// receiving the room's context, so the server records no context pull.
// method is GET or POST; path is relative to the incident.
func (c *Client) ReadJSON(ctx context.Context, method, path string, body map[string]any) (json.RawMessage, error) {
	var out json.RawMessage
	var err error
	switch method {
	case http.MethodGet, "":
		err = c.get(ctx, path, &out)
	case http.MethodPost:
		err = c.post(ctx, path, body, &out)
	default:
		return nil, fmt.Errorf("%s → method %s is not a read", path, method)
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}
