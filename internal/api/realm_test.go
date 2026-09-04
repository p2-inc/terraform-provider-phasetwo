package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestIDFromLocation(t *testing.T) {
	tests := []struct{ loc, want string }{
		{"https://app.phasetwo.io/auth/realms/self/deployments/d123", "d123"},
		{"/auth/realms/self/deployments/d123", "d123"},
		{"https://app.phasetwo.io/auth/realms/self/deployments/d123/", "d123"},
		{"d123", "d123"},
		{"", ""},
		{"https://app.phasetwo.io/", ""},
	}
	for _, tt := range tests {
		if got := idFromLocation(tt.loc); got != tt.want {
			t.Errorf("idFromLocation(%q) = %q, want %q", tt.loc, got, tt.want)
		}
	}
}

// Realm creation answers 201 with no body, so the id has to come from the Location header.
func TestCreateRealmUsesLocationHeader(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "https://example.test/auth/realms/self/deployments/d42")
		w.WriteHeader(http.StatusCreated)
	}))
	id, err := c.CreateRealm(context.Background(), "c1", "demo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "d42" {
		t.Errorf("id = %q, want d42", id)
	}
}

// Without a Location header the client falls back to finding the realm by name. The API
// lowercases names on create, so the lookup must use the lowered form.
func TestCreateRealmFallsBackToListing(t *testing.T) {
	var searched string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated) // no Location
			return
		}
		searched = r.URL.Query().Get("search")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"d99","name":"mixedcase","state":"PENDING"}]`))
	}))

	id, err := c.CreateRealm(context.Background(), "c1", "MixedCase")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "d99" {
		t.Errorf("id = %q, want d99", id)
	}
	if searched != "mixedcase" {
		t.Errorf("search = %q, want the lowercased name", searched)
	}
}

func TestCreateRealmErrorsWhenIDCannotBeFound(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	if _, err := c.CreateRealm(context.Background(), "c1", "demo"); err == nil {
		t.Fatal("expected an error when the realm's id cannot be determined")
	}
}

// deployment.update ignores everything but display_name, but still validates the request first:
// validateDeployment rejects a blank name and a null organizationId. So the update has to echo
// the current values back, or it is a guaranteed 400. This pins that down, because sending only
// display_name looks obviously correct and fails every time.
func TestUpdateRealmEchoesFieldsRequiredByValidation(t *testing.T) {
	var body map[string]any
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"d1","name":"demo","state":"ACTIVE","org_id":"org1"}`))
			return
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.WriteHeader(http.StatusNoContent)
	}))

	if err := c.UpdateRealm(context.Background(), "d1", "Nice Name"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body["display_name"] != "Nice Name" {
		t.Errorf("display_name = %v, want %q", body["display_name"], "Nice Name")
	}
	if body["name"] != "demo" {
		t.Errorf("name = %v, want %q — a blank name fails validateDeploymentName", body["name"], "demo")
	}
	if body["org_id"] != "org1" {
		t.Errorf("org_id = %v, want %q — a null one fails validateDeployment", body["org_id"], "org1")
	}
}

// A realm with no organization cannot satisfy the update endpoint's validation, so say so
// rather than sending a request that is certain to 400.
func TestUpdateRealmErrorsWithoutOrganization(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"d1","name":"demo","state":"ACTIVE"}`))
			return
		}
		t.Error("update should not have been attempted")
		w.WriteHeader(http.StatusNoContent)
	}))

	err := c.UpdateRealm(context.Background(), "d1", "Nice Name")
	if err == nil {
		t.Fatal("expected an error when the realm has no organization")
	}
	if !strings.Contains(err.Error(), "organization") {
		t.Errorf("error should mention the missing organization, got: %v", err)
	}
}

func TestWaitForRealmActiveSurfacesStateError(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"d1","name":"demo","state":"FAILED",` +
			`"stateError":"database unreachable"}`))
	}))
	_, err := c.WaitForRealmActive(context.Background(), "d1", time0)
	if err == nil {
		t.Fatal("expected an error for a FAILED realm")
	}
	if !strings.Contains(err.Error(), "database unreachable") {
		t.Errorf("error should include stateError, got: %v", err)
	}
}

func TestValidEnvVarName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"KC_SPI_MY_PROVIDER_ENABLED", true}, // custom SPI config is allowed
		{"KC_SPI_", true},
		{"MY_VAR", true}, // not a Keycloak variable at all
		{"DATABASE_URL", true},
		{"KC_DB_URL", false}, // built-in Keycloak variable
		{"KC_HOSTNAME", false},
		{"KC_", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := ValidEnvVarName(tt.name); got != tt.want {
			t.Errorf("ValidEnvVarName(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestVersionDependent(t *testing.T) {
	if !VersionDependent(ExtTheme) || !VersionDependent(ExtExtension) {
		t.Error("themes and extensions are tied to a Keycloak major version")
	}
	if VersionDependent(ExtPasswordBlacklist) || VersionDependent(ExtWellKnown) {
		t.Error("password blacklists and well-known files are version-independent")
	}
}
