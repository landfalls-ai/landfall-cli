package auth

// cli_refresh_test.go ports test/cli-refresh.test.mjs: silent refresh under
// the skew window, refresh targeting the token's OWN minting instance (never
// whatever's currently resolved), camelCase response-key mapping, and
// logout's revoke-then-clear behaviour. login()'s own browser-handoff flow is
// deliberately not exercised here either, for the same reason the Node
// source gives: openBrowser really does spawn a real browser, and nothing in
// this suite should pop a window on a developer's machine or a CI runner.
import (
	"context"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/instance"
)

func futureRFC3339(d time.Duration) string {
	return time.Now().Add(d).UTC().Format(time.RFC3339)
}

func TestGetCachedAccessToken_ReturnsTheCachedTokenUntouchedWhenNotNearExpiry(t *testing.T) {
	withCredentials(t, &Credentials{
		AccessToken:  "still-good",
		RefreshToken: ptrString("lf_refresh_x"),
		ExpiresAt:    ptrInt64(time.Now().Add(10 * time.Minute).UnixMilli()),
		Instance:     &instance.Instance{API: testAPI},
	})

	d := &fakeDoer{handler: func(int, string, map[string]any) (*http.Response, error) {
		t.Fatal("must not be called: a fresh token must not trigger any network call")
		return nil, nil
	}}

	token := GetCachedAccessToken(context.Background(), d)
	if token != "still-good" {
		t.Fatalf("token = %q, want %q", token, "still-good")
	}
	if len(d.calls) != 0 {
		t.Fatalf("calls = %d, want 0", len(d.calls))
	}
}

func TestGetCachedAccessToken_SilentlyRefreshesAnExpiredTokenAndPersistsTheRotatedPair(t *testing.T) {
	withCredentials(t, &Credentials{
		AccessToken:  "expired",
		RefreshToken: ptrString("lf_refresh_old"),
		ExpiresAt:    ptrInt64(time.Now().Add(-time.Second).UnixMilli()),
		OrgSlug:      ptrString("acme"),
		Instance:     &instance.Instance{API: testAPI},
	})

	d := &fakeDoer{handler: func(_ int, url string, body map[string]any) (*http.Response, error) {
		if want := testAPI + "/auth/cli-refresh"; url != want {
			t.Errorf("url = %q, want %q", url, want)
		}
		if body["refreshToken"] != "lf_refresh_old" {
			t.Errorf("body = %v, want refreshToken=lf_refresh_old", body)
		}
		return jsonResponse(t, 200, map[string]any{
			"accessToken":  "new-access",
			"refreshToken": "lf_refresh_new",
			"expiresAt":    futureRFC3339(time.Hour),
		}), nil
	}}

	token := GetCachedAccessToken(context.Background(), d)
	if token != "new-access" {
		t.Fatalf("token = %q, want new-access (the caller gets a working token with no visible prompt)", token)
	}
	if len(d.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(d.calls))
	}

	saved := readCredentialsRaw(t)
	if saved["access_token"] != "new-access" {
		t.Errorf("access_token = %v", saved["access_token"])
	}
	if saved["refresh_token"] != "lf_refresh_new" {
		t.Errorf("refresh_token = %v, want the rotated value — the OLD refresh token must be gone from disk", saved["refresh_token"])
	}
	if saved["org_slug"] != "acme" {
		t.Errorf("org_slug = %v, want the unrelated field to survive the refresh", saved["org_slug"])
	}
	expiresAt, ok := saved["expires_at"].(float64)
	if !ok || int64(expiresAt) <= time.Now().UnixMilli() {
		t.Errorf("expires_at = %v, want a future MILLISECOND epoch", saved["expires_at"])
	}
}

func TestGetCachedAccessToken_RefreshesProactivelyWithinTheSkewWindow(t *testing.T) {
	withCredentials(t, &Credentials{
		AccessToken:  "about-to-expire",
		RefreshToken: ptrString("lf_refresh_old"),
		ExpiresAt:    ptrInt64(time.Now().Add(30 * time.Second).UnixMilli()), // inside the 60s skew, not yet expired
		Instance:     &instance.Instance{API: testAPI},
	})

	d := &fakeDoer{handler: func(int, string, map[string]any) (*http.Response, error) {
		return jsonResponse(t, 200, map[string]any{
			"accessToken":  "new-access",
			"refreshToken": "lf_refresh_new",
			"expiresAt":    futureRFC3339(time.Hour),
		}), nil
	}}

	token := GetCachedAccessToken(context.Background(), d)
	if token != "new-access" {
		t.Fatalf("token = %q, want new-access", token)
	}
	if len(d.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(d.calls))
	}
}

func TestGetCachedAccessToken_ARefusedRefreshFallsBackToEmptyNotAPanic(t *testing.T) {
	withCredentials(t, &Credentials{
		AccessToken:  "expired",
		RefreshToken: ptrString("lf_refresh_dead"),
		ExpiresAt:    ptrInt64(time.Now().Add(-time.Second).UnixMilli()),
		Instance:     &instance.Instance{API: testAPI},
	})

	d := &fakeDoer{handler: func(int, string, map[string]any) (*http.Response, error) {
		return jsonResponse(t, 401, map[string]any{"error": "authentication_failed"}), nil
	}}

	token := GetCachedAccessToken(context.Background(), d)
	if token != "" {
		t.Fatalf("token = %q, want empty — the caller falls through to ExplainExpiredCredential, no crash", token)
	}
}

func TestRefreshAccessToken_ANetworkErrorWhileRefreshingIsTreatedAsCouldNotRefresh(t *testing.T) {
	withCredentials(t, &Credentials{
		AccessToken:  "expired",
		RefreshToken: ptrString("lf_refresh_x"),
		ExpiresAt:    ptrInt64(time.Now().Add(-time.Second).UnixMilli()),
		Instance:     &instance.Instance{API: testAPI},
	})

	d := &fakeDoer{handler: func(int, string, map[string]any) (*http.Response, error) {
		return nil, errors.New("ENOTFOUND")
	}}

	if got := RefreshAccessToken(context.Background(), d); got != "" {
		t.Fatalf("RefreshAccessToken = %q, want empty (never thrown)", got)
	}
}

func TestGetCachedAccessToken_ALegacyCredentialWithNoRefreshTokenDeclinesWithoutANetworkCall(t *testing.T) {
	withCredentials(t, &Credentials{
		AccessToken: "expired",
		ExpiresAt:   ptrInt64(time.Now().Add(-time.Second).UnixMilli()),
		Iss:         "landfall-core",
	})

	d := &fakeDoer{handler: func(int, string, map[string]any) (*http.Response, error) {
		t.Fatal("must not be called")
		return nil, nil
	}}

	if got := GetCachedAccessToken(context.Background(), d); got != "" {
		t.Fatalf("GetCachedAccessToken = %q, want empty", got)
	}
	if len(d.calls) != 0 {
		t.Fatalf("calls = %d, want 0", len(d.calls))
	}
}

func TestRefreshAccessToken_ACredentialWithARefreshTokenButNoRecordedInstanceDeclines(t *testing.T) {
	// Nothing to target: presenting the refresh token to whatever instance
	// happens to be currently resolved is exactly what recording the minting
	// instance exists to prevent.
	withCredentials(t, &Credentials{
		AccessToken:  "expired",
		RefreshToken: ptrString("lf_refresh_x"),
		ExpiresAt:    ptrInt64(time.Now().Add(-time.Second).UnixMilli()),
		// Instance intentionally left nil.
	})

	d := &fakeDoer{handler: func(int, string, map[string]any) (*http.Response, error) {
		t.Fatal("must not be called")
		return nil, nil
	}}

	if got := RefreshAccessToken(context.Background(), d); got != "" {
		t.Fatalf("RefreshAccessToken = %q, want empty", got)
	}
	if len(d.calls) != 0 {
		t.Fatalf("calls = %d, want 0", len(d.calls))
	}
}

func TestLogout_RevokesServerSideThenClearsTheLocalFile(t *testing.T) {
	withCredentials(t, &Credentials{
		AccessToken:  "x",
		RefreshToken: ptrString("lf_refresh_x"),
		ExpiresAt:    ptrInt64(time.Now().Add(10 * time.Minute).UnixMilli()),
		Instance:     &instance.Instance{API: testAPI},
	})

	d := &fakeDoer{handler: func(_ int, url string, body map[string]any) (*http.Response, error) {
		if want := testAPI + "/auth/cli-logout"; url != want {
			t.Errorf("url = %q, want %q", url, want)
		}
		if body["refreshToken"] != "lf_refresh_x" {
			t.Errorf("body = %v, want refreshToken=lf_refresh_x", body)
		}
		return jsonResponse(t, 204, nil), nil
	}}

	if err := Logout(context.Background(), d); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if len(d.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(d.calls))
	}
	if _, err := os.Stat(CachePath()); !os.IsNotExist(err) {
		t.Fatalf("credentials.json still present after logout (err=%v)", err)
	}
}

func TestLogout_StillClearsTheLocalFileWhenTheRevokeCallFails(t *testing.T) {
	withCredentials(t, &Credentials{
		AccessToken:  "x",
		RefreshToken: ptrString("lf_refresh_x"),
		ExpiresAt:    ptrInt64(time.Now().Add(10 * time.Minute).UnixMilli()),
		Instance:     &instance.Instance{API: testAPI},
	})

	d := &fakeDoer{handler: func(int, string, map[string]any) (*http.Response, error) {
		return nil, errors.New("offline")
	}}

	if err := Logout(context.Background(), d); err != nil {
		t.Fatalf("Logout: %v, want nil — offline sign-out must still succeed", err)
	}
	if _, err := os.Stat(CachePath()); !os.IsNotExist(err) {
		t.Fatalf("credentials.json still present after logout (err=%v)", err)
	}
}

func TestLogout_ALegacyCredentialWithNoRefreshTokenSkipsTheNetworkCall(t *testing.T) {
	withCredentials(t, &Credentials{
		AccessToken: "x",
		ExpiresAt:   ptrInt64(time.Now().Add(10 * time.Minute).UnixMilli()),
		Iss:         "landfall-core",
	})

	d := &fakeDoer{handler: func(int, string, map[string]any) (*http.Response, error) {
		t.Fatal("must not be called")
		return nil, nil
	}}

	if err := Logout(context.Background(), d); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if len(d.calls) != 0 {
		t.Fatalf("calls = %d, want 0", len(d.calls))
	}
}

func TestLogout_WithNoCachedSessionAtAllIsASilentNoOp(t *testing.T) {
	withCredentials(t, nil)

	d := &fakeDoer{handler: func(int, string, map[string]any) (*http.Response, error) {
		t.Fatal("must not be called")
		return nil, nil
	}}

	if err := Logout(context.Background(), d); err != nil {
		t.Fatalf("Logout: %v, want nil", err)
	}
}
