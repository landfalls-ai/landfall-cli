package auth

// testutil_test.go collects the fakes shared by every *_test.go in this
// package — the Go analogues of the Node ancestor's fakeFetch/jsonResponse/
// withCredentials helpers, duplicated verbatim across
// test/cli-refresh.test.mjs and test/cli-handoff-poll.test.mjs.
//
// testAPI is deliberately a fictional domain, never landfalls.ai or a
// loopback address: internal/guard/no_hardcoded_hostname_test.go forbids
// naming either outside internal/instance, and this package is not exempt.
import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// testAPI stands in for "whichever Landfall minted this credential" — its
// exact value is never asserted on for its own sake, only echoed back in
// request-shape assertions.
const testAPI = "https://instance.example/api"

// doerFunc adapts a plain function to the Doer interface, mirroring the Node
// suite's injected fetchImpl.
type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(req *http.Request) (*http.Response, error) { return f(req) }

// recordedCall captures one request the fake Doer observed, so a test can
// assert both the outcome and the request shape — the Go analogue of the
// Node suite's fakeFetch(handler).calls.
type recordedCall struct {
	url  string
	body map[string]any
}

// fakeDoer records every call and delegates the response to handler, whose
// first argument is the 1-based call number — matching the `calls.length`
// the Node suite's fakeFetch passes its handler in
// cli-handoff-poll.test.mjs.
type fakeDoer struct {
	calls   []recordedCall
	handler func(callNumber int, url string, body map[string]any) (*http.Response, error)
}

func (f *fakeDoer) Do(req *http.Request) (*http.Response, error) {
	var body map[string]any
	if req.Body != nil {
		raw, _ := io.ReadAll(req.Body)
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &body)
		}
	}
	f.calls = append(f.calls, recordedCall{url: req.URL.String(), body: body})
	return f.handler(len(f.calls), req.URL.String(), body)
}

// jsonResponse builds an *http.Response the way the Node suite's
// jsonResponse(status, body) fakes fetch's Response shape.
func jsonResponse(t *testing.T, status int, body any) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encoding fake response body: %v", err)
		}
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(&buf)}
}

// malformedJSONResponse fakes a 200 whose body cannot be decoded, matching
// the Node suite's `json: async () => { throw new SyntaxError(...) }`.
func malformedJSONResponse(status int) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader([]byte("{not json")))}
}

// withCredentials points XDG_CONFIG_HOME at a fresh per-test directory and,
// when creds is non-nil, writes it as credentials.json before the test body
// runs — mirroring the Node suite's withCredentials(credentials, fn) helper.
// Auto-restored and auto-cleaned via t.Setenv/t.TempDir, so no test ever
// touches the developer's own cached session.
func withCredentials(t *testing.T, creds *Credentials) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if creds == nil {
		return
	}
	dir := filepath.Dir(CachePath())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("creating %s: %v", dir, err)
	}
	data, err := json.Marshal(creds)
	if err != nil {
		t.Fatalf("marshaling credentials: %v", err)
	}
	if err := os.WriteFile(CachePath(), data, 0o600); err != nil {
		t.Fatalf("writing %s: %v", CachePath(), err)
	}
}

// readCredentialsRaw reads the current credentials.json as a generic map, so
// a test can assert on the snake_case wire format (the Node ancestor's own
// file format contract) without going back through this package's own
// (un)marshaling to check it.
func readCredentialsRaw(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(CachePath())
	if err != nil {
		t.Fatalf("reading %s: %v", CachePath(), err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parsing %s: %v", CachePath(), err)
	}
	return m
}

func ptrString(s string) *string { return &s }
func ptrInt64(n int64) *int64    { return &n }
