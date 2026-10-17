package client

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func testGov() (*governor, *clock) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	return &governor{now: c.now, jitter: func() float64 { return 0 }}, c
}

func TestGovernorBacksOffExponentiallyAndCaps(t *testing.T) {
	g, c := testGov()
	var got []time.Duration
	for i := 0; i < 7; i++ {
		g.refused(0)
		got = append(got, g.cooldown())
		c.advance(g.cooldown())
	}
	want := []time.Duration{2, 4, 8, 16, 30, 30, 30}
	for i, w := range want {
		if got[i] != w*time.Second {
			t.Fatalf("refusal %d: cool-down %v, want %v", i+1, got[i], w*time.Second)
		}
	}
}

func TestGovernorHonoursRetryAfterButNotAbsurdOnes(t *testing.T) {
	g, _ := testGov()
	g.refused(10 * time.Second)
	if g.cooldown() != 10*time.Second {
		t.Fatalf("cool-down %v, want the 10 s the edge asked for", g.cooldown())
	}
	if parseRetryAfter("3600") != govRetryAfterMax {
		t.Fatalf("an hour-long Retry-After must be capped")
	}
	if parseRetryAfter("soon") != 0 || parseRetryAfter("-5") != 0 {
		t.Fatalf("unreadable Retry-After must be ignored")
	}
}

func TestGovernorResetsOnAnyAnswerThatIsNotARefusal(t *testing.T) {
	g, _ := testGov()
	g.refused(0)
	g.refused(0)
	g.ok()
	if g.cooldown() != 0 {
		t.Fatalf("cool-down must end on success")
	}
	g.refused(0)
	if g.cooldown() != govBase {
		t.Fatalf("the next refusal must start from the base again, got %v", g.cooldown())
	}
}

func TestGovernedDoerStopsSendingAfter429AndResumesAfterCooldown(t *testing.T) {
	var hits int32
	var refuse atomic32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		if refuse.get() {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	g, c := testGov()
	gs := &governors{byHost: map[string]*governor{}}
	d := &governedDoer{inner: srv.Client(), gs: gs}
	gs.byHost[strings.TrimPrefix(srv.URL, "http://")] = g
	do := func() *http.Response {
		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		res, err := d.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		return res
	}

	refuse.set(true)
	if do().StatusCode != 429 {
		t.Fatal("first call should see the real 429")
	}
	for i := 0; i < 50; i++ {
		res := do()
		if res.StatusCode != 429 || res.Header.Get("X-Landfall-Local") != "backoff" {
			t.Fatalf("calls during the cool-down must be answered locally, got %d", res.StatusCode)
		}
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("the server saw %d requests; 50 follow-ups must not reach it", n)
	}

	refuse.set(false)
	c.advance(3 * time.Second)
	if do().StatusCode != 200 {
		t.Fatal("after the cool-down the next call goes out and succeeds")
	}
	if g.cooldown() != 0 {
		t.Fatal("success must clear the cool-down")
	}
}

func TestLocalRefusalReadsAsARetryableHTTPError(t *testing.T) {
	// The caller-side contract: a 429 is "not now", never a refusal to give up on.
	req, _ := http.NewRequest(http.MethodGet, "http://x/y", nil)
	res := localRefusal(req, 1500*time.Millisecond)
	if res.StatusCode != 429 || res.Header.Get("Retry-After") != "2" {
		t.Fatalf("got %d / Retry-After %q", res.StatusCode, res.Header.Get("Retry-After"))
	}
	if _, isRefusal := Refusal(&HTTPError{Path: "/y", Status: res.StatusCode}); isRefusal {
		t.Fatal("a 429 must not be treated as a permanent refusal")
	}
	if !strings.Contains((&HTTPError{Path: "/y", Status: 429}).Error(), "429") {
		t.Fatal("error text should carry the status")
	}
}

type atomic32 struct{ v int32 }

func (a *atomic32) set(b bool) {
	if b {
		atomic.StoreInt32(&a.v, 1)
	} else {
		atomic.StoreInt32(&a.v, 0)
	}
}
func (a *atomic32) get() bool { return atomic.LoadInt32(&a.v) == 1 }

func TestGovernorsAreIsolatedPerHost(t *testing.T) {
	// A refusal from one Landfall (or one test server) must not slow another.
	var refusing, fine int32
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&refusing, 1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&fine, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer b.Close()

	d := &governedDoer{inner: http.DefaultClient, gs: &governors{}}
	get := func(url string) int {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		res, err := d.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		return res.StatusCode
	}
	for i := 0; i < 2; i++ {
		if get(a.URL) != 429 {
			t.Fatal("host A should be refusing")
		}
	}
	if get(b.URL) != 200 {
		t.Fatal("host B must be unaffected by host A's cool-down")
	}
	if atomic.LoadInt32(&refusing) != 1 {
		t.Fatalf("host A saw %d requests, want 1", atomic.LoadInt32(&refusing))
	}
}
