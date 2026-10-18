package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestReadJSONIsAPersonsRead(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("authorization")+" "+string(body))
		_, _ = w.Write([]byte(`{"ok":1}`))
	}))
	defer srv.Close()
	c := New(Config{BaseURL: srv.URL, Slug: "acme", IncidentID: "inc-1", Token: "room"}, nil)
	c.SetAgentInstanceID("inst-1")
	ctx := context.Background()
	if raw, err := c.ReadJSON(ctx, http.MethodGet, "/events?limit=5", nil); err != nil || string(raw) != `{"ok":1}` {
		t.Fatalf("get: %s %v", raw, err)
	}
	if _, err := c.ReadJSON(ctx, http.MethodPost, "/widgets/data", map[string]any{"widgetIds": []string{"w1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ReadJSON(ctx, http.MethodDelete, "/x", nil); err == nil {
		t.Fatal("a delete is not a read")
	}
	if got[0] != "GET /o/acme/incidents/inc-1/events?limit=5 Bearer room " {
		t.Fatalf("get request %q: a person's read names no agent instance", got[0])
	}
	if got[1] != `POST /o/acme/incidents/inc-1/widgets/data Bearer room {"widgetIds":["w1"]}` {
		t.Fatalf("post request %q", got[1])
	}
}

func TestOrgReader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("authorization") != "Bearer person" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /o/acme/incidents":
			if r.URL.Query().Get("limit") != "200" || r.URL.Query().Get("cursor") != "a b" {
				t.Errorf("query = %q", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"items":[{"id":"i1","displayId":"Acme 1","title":null,"status":"open","severity":"sev1","createdAt":"2026-10-08T15:00:00Z"}],"nextCursor":null}`))
		case "POST /o/acme/incidents/i1/edge/share-link":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"shareUrl":"https://app/j/x"}`))
		case "POST /o/acme/incidents/i2/edge/share-link":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
		case "GET /o/acme/memory/entries":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"not a member"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	o := NewOrgReader(srv.URL+"/", "acme", "person", nil)
	page, err := o.ListIncidents(ctx, IncidentListQuery{Limit: 999, Cursor: "a b"})
	if err != nil || len(page.Items) != 1 || page.Items[0].Title != nil || *page.Items[0].Severity != "sev1" || page.NextCursor != nil {
		t.Fatalf("list: %+v %v", page, err)
	}
	if m, err := o.MintShareLink(ctx, "i1"); err != nil || m.ShareURL != "https://app/j/x" {
		t.Fatalf("mint: %+v %v", m, err)
	}
	if _, err := o.MintShareLink(ctx, "i2"); err == nil {
		t.Fatal("a mint with no link is an error")
	}
	_, err = o.MemoryEntries(ctx)
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusForbidden || he.Reason != "not a member" {
		t.Fatalf("refusal: %v", err)
	}
	if _, err := NewOrgReader(srv.URL, "acme", "wrong", nil).WidgetsData(ctx, "i1", []string{"w"}); !errors.As(err, &he) || he.Status != http.StatusUnauthorized {
		t.Fatalf("unauthorized: %v", err)
	}
}

// TestListIncidentsQuery: what the list route is sent. Status and search are
// left out when empty, so a request without them is the one older servers
// have always seen, and are escaped when set.
func TestListIncidentsQuery(t *testing.T) {
	cases := []struct {
		name      string
		query     IncidentListQuery
		wantRaw   string
		wantParam map[string]string
	}{
		{"nothing but a limit", IncidentListQuery{Limit: 50}, "limit=50", map[string]string{"limit": "50"}},
		{"a limit under one is one", IncidentListQuery{}, "limit=1", map[string]string{"limit": "1"}},
		{"a limit over 200 is 200", IncidentListQuery{Limit: 999}, "limit=200", map[string]string{"limit": "200"}},
		{"status", IncidentListQuery{Limit: 200, Status: "open"}, "limit=200&status=open", map[string]string{"status": "open"}},
		{"search with a space", IncidentListQuery{Limit: 200, Q: "disk full"}, "limit=200&q=disk%20full", map[string]string{"q": "disk full"}},
		{"search with reserved characters", IncidentListQuery{Limit: 200, Q: "a&b=c#d+e"}, "limit=200&q=a%26b%3Dc%23d%2Be", map[string]string{"q": "a&b=c#d+e"}},
		{"search in another script", IncidentListQuery{Limit: 200, Q: "תקלה"}, "limit=200&q=%D7%AA%D7%A7%D7%9C%D7%94", map[string]string{"q": "תקלה"}},
		{"everything", IncidentListQuery{Limit: 200, Cursor: "c 2", Status: "closed", Q: "12"}, "limit=200&cursor=c%202&status=closed&q=12", map[string]string{"cursor": "c 2", "status": "closed", "q": "12"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var raw string
			var parsed url.Values
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, parsed = r.URL.RawQuery, r.URL.Query()
				_, _ = w.Write([]byte(`{"items":[],"nextCursor":null}`))
			}))
			defer srv.Close()
			if _, err := NewOrgReader(srv.URL, "acme", "person", nil).ListIncidents(context.Background(), tc.query); err != nil {
				t.Fatal(err)
			}
			if raw != tc.wantRaw {
				t.Fatalf("raw query = %q, want %q", raw, tc.wantRaw)
			}
			for k, v := range tc.wantParam {
				if parsed.Get(k) != v {
					t.Fatalf("%s = %q, want %q", k, parsed.Get(k), v)
				}
			}
			for _, k := range []string{"cursor", "status", "q"} {
				if _, want := tc.wantParam[k]; !want && parsed.Has(k) {
					t.Fatalf("%s must not be sent: %q", k, raw)
				}
			}
		})
	}
}
