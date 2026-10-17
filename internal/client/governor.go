package client

import (
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The edge refuses a client address past 60 requests per 10 seconds, and
// everything behind one address shares that allowance: a person's terminal
// agents, the Edge app, a teammate on the same network. A 429 is the edge
// saying "not now", and the only useful answer is to stop asking for a while.
//
// Before this, nothing did. The daemon's pollers (the watched person read every
// 1.5 s per room, the get_updates fallback, the spool sweeps) each kept their
// own clock, and a failed read does not slow the next tick, so one address
// stayed over the limit for days: 3.5 million refusals from one daemon in
// three days, each refusal costing the next attempt of every other client on
// that address too. Refusing from the edge is the safety net; this is the
// client behaving as the other half of the contract.
//
// THE RULE. After a 429, every request this process would make is answered
// locally with a 429 (no network call) until a cool-down ends. The cool-down
// doubles with each consecutive refusal, from govBase up to govMax, gets a
// little jitter so processes sharing an address do not come back in step, and
// never undercuts a Retry-After the edge sent. Any answer that is not a 429
// ends it. Callers need no change: they already treat a 429 as "not now"
// (see Refusal), and the one the governor makes up is indistinguishable.
//
// One governor per edge host, shared by the whole process, because the
// allowance is per address and host, not per room or per session.
const (
	govBase = 2 * time.Second
	govMax  = 30 * time.Second
	// A Retry-After longer than this is not believed: it would park the whole
	// process for what is, to the edge, a one-second window.
	govRetryAfterMax = 60 * time.Second
)

type governor struct {
	mu       sync.Mutex
	until    time.Time
	failures int
	now      func() time.Time
	jitter   func() float64 // in [0,1)
}

func newGovernor() *governor {
	return &governor{now: time.Now, jitter: rand.Float64}
}

// cooldown is how long requests are being answered locally, zero when free.
func (g *governor) cooldown() time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	if left := g.until.Sub(g.now()); left > 0 {
		return left
	}
	return 0
}

// refused records a 429 and starts (or extends) the cool-down.
func (g *governor) refused(retryAfter time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failures++
	wait := govBase
	for i := 1; i < g.failures && wait < govMax; i++ {
		wait *= 2
	}
	if wait > govMax {
		wait = govMax
	}
	wait += time.Duration(float64(wait) * 0.25 * g.jitter())
	if retryAfter > wait {
		wait = retryAfter
	}
	g.until = g.now().Add(wait)
}

// ok ends the cool-down: the edge answered something other than a refusal.
func (g *governor) ok() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failures = 0
	g.until = time.Time{}
}

// governors holds one governor per edge host, so a refusal from one Landfall
// never slows requests to another (and a test server never slows the next
// test's).
type governors struct {
	mu     sync.Mutex
	byHost map[string]*governor
}

func (gs *governors) forHost(host string) *governor {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	if gs.byHost == nil {
		gs.byHost = map[string]*governor{}
	}
	g, ok := gs.byHost[host]
	if !ok {
		g = newGovernor()
		gs.byHost[host] = g
	}
	return g
}

// processGovernors is shared by every client this process builds with the
// default transport.
var processGovernors = &governors{}

// governedDoer wraps a transport with a per-host governor.
type governedDoer struct {
	inner Doer
	gs    *governors
}

func (d *governedDoer) Do(req *http.Request) (*http.Response, error) {
	g := d.gs.forHost(req.URL.Host)
	if left := g.cooldown(); left > 0 {
		return localRefusal(req, left), nil
	}
	res, err := d.inner.Do(req)
	if err != nil {
		return res, err
	}
	if res.StatusCode == http.StatusTooManyRequests {
		g.refused(parseRetryAfter(res.Header.Get("Retry-After")))
	} else {
		g.ok()
	}
	return res, nil
}

// localRefusal is the 429 the governor answers with while cooling down.
func localRefusal(req *http.Request, left time.Duration) *http.Response {
	secs := int(left/time.Second) + 1
	h := http.Header{}
	h.Set("Retry-After", strconv.Itoa(secs))
	h.Set("X-Landfall-Local", "backoff")
	return &http.Response{
		Status:     "429 Too Many Requests",
		StatusCode: http.StatusTooManyRequests,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     h,
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
	}
}

// parseRetryAfter reads the seconds form; the date form is not used by the
// edge and is ignored.
func parseRetryAfter(v string) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return 0
	}
	d := time.Duration(n) * time.Second
	if d > govRetryAfterMax {
		return govRetryAfterMax
	}
	return d
}
