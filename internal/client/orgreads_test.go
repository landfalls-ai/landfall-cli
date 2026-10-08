package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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
	page, err := o.ListIncidents(ctx, 999, "a b")
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
