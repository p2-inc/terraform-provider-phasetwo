package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestConfigURLs(t *testing.T) {
	tests := []struct {
		name      string
		cfg       Config
		wantAPI   string
		wantToken string
	}{
		{
			name:      "defaults to production",
			cfg:       Config{},
			wantAPI:   "https://app.phasetwo.io/auth/realms/self/v2",
			wantToken: "https://app.phasetwo.io/auth/realms/self/protocol/openid-connect/token",
		},
		{
			name:      "staging environment",
			cfg:       Config{Environment: "app-staging"},
			wantAPI:   "https://app-staging.phasetwo.io/auth/realms/self/v2",
			wantToken: "https://app-staging.phasetwo.io/auth/realms/self/protocol/openid-connect/token",
		},
		{
			name:      "base_url overrides environment",
			cfg:       Config{Environment: "app-staging", BaseURL: "http://localhost:8080/auth"},
			wantAPI:   "http://localhost:8080/auth/realms/self/v2",
			wantToken: "http://localhost:8080/auth/realms/self/protocol/openid-connect/token",
		},
		{
			name:      "trailing slash on base_url is not doubled",
			cfg:       Config{BaseURL: "http://localhost:8080/auth/"},
			wantAPI:   "http://localhost:8080/auth/realms/self/v2",
			wantToken: "http://localhost:8080/auth/realms/self/protocol/openid-connect/token",
		},
		{
			name:      "custom realm",
			cfg:       Config{Realm: "other"},
			wantAPI:   "https://app.phasetwo.io/auth/realms/other/v2",
			wantToken: "https://app.phasetwo.io/auth/realms/other/protocol/openid-connect/token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.APIBase(); got != tt.wantAPI {
				t.Errorf("APIBase() = %q, want %q", got, tt.wantAPI)
			}
			if got := tt.cfg.TokenURL(); got != tt.wantToken {
				t.Errorf("TokenURL() = %q, want %q", got, tt.wantToken)
			}
		})
	}
}

// The v2 segment is the Keycloak realm-resource-provider id. Without it, requests land on the
// legacy `clusters` provider, which is a different API. Guard it explicitly.
func TestAPIBaseIncludesV2(t *testing.T) {
	got := Config{}.APIBase()
	if want := "/v2"; len(got) < len(want) || got[len(got)-len(want):] != want {
		t.Fatalf("APIBase() = %q, must end in %q", got, want)
	}
}

func TestNewRequiresCredentials(t *testing.T) {
	if _, err := New(context.Background(), Config{}); err == nil {
		t.Fatal("expected an error with no credentials")
	}
	if _, err := New(context.Background(), Config{ClientID: "id"}); err == nil {
		t.Fatal("expected an error with a client id but no secret")
	}
	if _, err := New(context.Background(), Config{AccessToken: "tok"}); err != nil {
		t.Fatalf("access token alone should be enough: %v", err)
	}
	if _, err := New(context.Background(), Config{ClientID: "id", ClientSecret: "sec"}); err != nil {
		t.Fatalf("client credentials should be enough: %v", err)
	}
}

func TestErrorClassification(t *testing.T) {
	tests := []struct {
		status                           int
		notFound, conflict, unauthorized bool
	}{
		{http.StatusNotFound, true, false, false},
		{http.StatusConflict, false, true, false},
		{http.StatusUnauthorized, false, false, true},
		{http.StatusForbidden, false, false, true},
		{http.StatusInternalServerError, false, false, false},
	}
	for _, tt := range tests {
		err := check("op", tt.status, nil)
		if err == nil {
			t.Fatalf("status %d should be an error", tt.status)
		}
		if IsNotFound(err) != tt.notFound {
			t.Errorf("status %d: IsNotFound = %v, want %v", tt.status, IsNotFound(err), tt.notFound)
		}
		if IsConflict(err) != tt.conflict {
			t.Errorf("status %d: IsConflict = %v, want %v", tt.status, IsConflict(err), tt.conflict)
		}
		if IsUnauthorized(err) != tt.unauthorized {
			t.Errorf("status %d: IsUnauthorized = %v, want %v",
				tt.status, IsUnauthorized(err), tt.unauthorized)
		}
	}
	if err := check("op", 200, nil); err != nil {
		t.Errorf("200 should not be an error, got %v", err)
	}
	if err := check("op", 204, nil); err != nil {
		t.Errorf("204 should not be an error, got %v", err)
	}
}

func TestErrorMessageExtraction(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{"error key", `{"error":"Name is taken or reserved."}`, "Name is taken or reserved."},
		{"errorMessage key", `{"errorMessage":"nope"}`, "nope"},
		{"error_description key", `{"error_description":"bad client"}`, "bad client"},
		{"message key", `{"message":"boom"}`, "boom"},
		{"plain text falls through", `not json`, "not json"},
		{"empty stays empty", ``, ""},
		{"unknown shape falls back to raw", `{"detail":"x"}`, `{"detail":"x"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := errorMessage([]byte(tt.body)); got != tt.want {
				t.Errorf("errorMessage(%q) = %q, want %q", tt.body, got, tt.want)
			}
		})
	}
}

func TestErrorMessageTruncates(t *testing.T) {
	long := make([]byte, 1000)
	for i := range long {
		long[i] = 'x'
	}
	got := errorMessage(long)
	if len(got) > 520 {
		t.Errorf("message not truncated: got %d bytes", len(got))
	}
}

// newTestClient points a Client at an httptest server, bypassing OAuth with a static token.
func newTestClient(t *testing.T, h http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	c, err := New(context.Background(), Config{
		BaseURL:     srv.URL + "/auth",
		AccessToken: "test-token",
		HTTPClient:  srv.Client(),
	})
	if err != nil {
		t.Fatalf("building test client: %v", err)
	}
	return c, srv
}
