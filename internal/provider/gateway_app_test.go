package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// fakeGateway is the Gateway part of the API in memory, with the behaviour the provider relies
// on: absent request fields keep their value, the origin cannot move while active, and
// activation needs a verified origin and a ready hostname.
type fakeGateway struct {
	mu   sync.Mutex
	apps map[string]map[string]any

	verifyResult string
	// notReadyReads is how many detail reads of a verified app report the hostname as not ready.
	notReadyReads int
	calls         []string
}

func newFakeGateway() *fakeGateway {
	return &fakeGateway{apps: map[string]map[string]any{}, verifyResult: "VERIFIED"}
}

func (f *fakeGateway) app(id string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.apps[id]
}

func (f *fakeGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	const prefix = "/auth/realms/self/v2/deployments/"
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, prefix), "/")
	if !strings.HasPrefix(r.URL.Path, prefix) || len(parts) < 2 || parts[1] != "apps" {
		http.NotFound(w, r)
		return
	}
	realm := parts[0]
	f.calls = append(f.calls, r.Method+" "+strings.Join(parts[1:], "/"))
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	conflict := func(msg string) { reply(http.StatusConflict, map[string]string{"error": msg}) }

	if len(parts) == 2 && r.Method == http.MethodPost {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		id := fmt.Sprintf("app-%d", len(f.apps)+1)
		host := req["public_hostname"].(string)
		app := map[string]any{
			"id": id, "deployment_id": realm, "public_hostname": host, "status": "PENDING",
			"revision": 0, "client_id": "gateway-" + host,
			"issuer":               "https://kc.example.com/realms/acme",
			"callback_url":         "https://" + host + "/_p2/auth/callback",
			"assertion_issuer":     "https://gateway.example.com",
			"jwks_url":             "https://gateway.example.com/.well-known/jwks.json",
			"origin_secret_header": "X-Phasetwo-Origin-Secret", "next_secret_pending": false,
			"health_path": "/", "policy": map[string]any{"default": "authenticated", "rules": []any{}},
			"header_mappings": map[string]any{}, "assertion_enabled": false,
			"assertion_claims":     []any{"email", "preferred_username"},
			"idle_timeout_seconds": 1800, "max_session_seconds": 28800,
			"created_at": 1, "updated_at": 1,
		}
		apply(app, req)
		f.apps[id] = app
		reply(http.StatusCreated, map[string]any{
			"app": app, "origin_secret": "p2os_test", "header_name": "X-Phasetwo-Origin-Secret",
		})
		return
	}
	if len(parts) < 3 {
		http.NotFound(w, r)
		return
	}
	app, ok := f.apps[parts[2]]
	if !ok || app["deployment_id"] != realm {
		reply(http.StatusNotFound, map[string]string{"error": "No protected app"})
		return
	}
	action := strings.Join(parts[3:], "/")
	switch {
	case action == "" && r.Method == http.MethodGet:
		detail := copyMap(app)
		verified := app["verification"] != nil &&
			app["verification"].(map[string]any)["result"] == "VERIFIED"
		ready := f.notReadyReads == 0
		if verified && !ready {
			f.notReadyReads--
		}
		status := "PENDING"
		if ready {
			status = "ACTIVE"
		}
		detail["readiness"] = map[string]any{
			"auth_configured": true, "origin_verified": verified, "hostname_ready": ready,
			"hostname": map[string]any{
				"status": status, "ssl_status": status, "dns_target": "apps.gateway.example.com",
				"dns_ok": ready,
			},
		}
		reply(http.StatusOK, detail)
	case action == "" && r.Method == http.MethodPut:
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if app["status"] == "ACTIVE" && req["origin_url"] != nil && req["origin_url"] != app["origin_url"] {
			conflict("deactivate the app before changing its origin")
			return
		}
		apply(app, req)
		reply(http.StatusOK, app)
	case action == "" && r.Method == http.MethodDelete:
		delete(f.apps, parts[2])
		w.WriteHeader(http.StatusNoContent)
	case action == "verify":
		app["verification"] = map[string]any{
			"result": f.verifyResult, "reason": "test", "secret": "current",
			"checked_at": "2026-11-10T00:00:00Z", "url": app["origin_url"].(string) + "/",
			"probes": []any{
				map[string]any{"name": "valid_secret", "status": 200},
				map[string]any{"name": "no_secret", "status": 200},
			},
		}
		reply(http.StatusOK, app)
	case action == "activate":
		v, _ := app["verification"].(map[string]any)
		if v == nil || v["result"] != "VERIFIED" {
			conflict("the origin has not been verified to require the secret")
			return
		}
		if f.notReadyReads > 0 {
			conflict("the hostname, its certificate or its DNS is not ready")
			return
		}
		app["status"] = "ACTIVE"
		reply(http.StatusOK, app)
	case action == "deactivate":
		if app["status"] == "ACTIVE" {
			app["status"] = "DISABLED"
		}
		reply(http.StatusOK, app)
	default:
		http.NotFound(w, r)
	}
}

// apply mirrors ProtectedAppSettings.apply: absent fields are unchanged, an empty
// post_logout_redirect_uri clears it, and the API stores the policy as sent.
func apply(app, req map[string]any) {
	for k, v := range req {
		if v == nil || k == "public_hostname" {
			continue
		}
		if k == "post_logout_redirect_uri" && v == "" {
			delete(app, k)
			continue
		}
		app[k] = v
	}
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func gatewayProviderConfig(url string) string {
	return fmt.Sprintf(`
provider "phasetwo" {
  base_url     = "%s/auth"
  access_token = "test"
}
`, url)
}

func TestGatewayAppLifecycle(t *testing.T) {
	gatewayPollInterval = 10 * time.Millisecond
	fake := newFakeGateway()
	srv := httptest.NewServer(fake)
	defer srv.Close()

	app := func(name, origin, extra string) string {
		return gatewayProviderConfig(srv.URL) + fmt.Sprintf(`
resource "phasetwo_gateway_app" "admin" {
  realm_id        = "realm-1"
  name            = %q
  public_hostname = "admin.example.com"
  origin_url      = %q
  health_path     = "/healthz"
  %s
}
`, name, origin, extra)
	}
	const origin = "https://admin-origin.example.com"
	withPolicy := `
  header_mappings = { "X-Phasetwo-Auth-Email" = "email" }
  assertion_enabled = true
  post_logout_redirect_uri = "https://example.com/bye"
  policy = {
    default = "deny"
    rules = [
      { path_prefix = "/", methods = ["GET"] },
      {
        path_prefix     = "/admin"
        realm_roles_any = ["admin"]
        claims_equal    = { email_verified = "true" }
      },
      { path_prefix = "/internal", effect = "deny" },
    ]
  }`
	activation := `
resource "phasetwo_gateway_app_activation" "admin" {
  realm_id = phasetwo_gateway_app.admin.realm_id
  app_id   = phasetwo_gateway_app.admin.id
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if len(fake.apps) != 0 {
				return fmt.Errorf("apps left behind: %v", fake.apps)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: app("Admin", origin, withPolicy),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("phasetwo_gateway_app.admin", "id", "app-1"),
					resource.TestCheckResourceAttr("phasetwo_gateway_app.admin", "origin_secret", "p2os_test"),
					resource.TestCheckResourceAttr("phasetwo_gateway_app.admin", "status", "PENDING"),
					resource.TestCheckResourceAttr("phasetwo_gateway_app.admin", "client_id", "gateway-admin.example.com"),
					resource.TestCheckResourceAttr("phasetwo_gateway_app.admin", "dns_target", "apps.gateway.example.com"),
					resource.TestCheckResourceAttr("phasetwo_gateway_app.admin", "assertion_claims.#", "2"),
					resource.TestCheckResourceAttr("phasetwo_gateway_app.admin", "policy.rules.1.claims_equal.email_verified", "true"),
					func(*terraform.State) error {
						rules := fake.app("app-1")["policy"].(map[string]any)["rules"].([]any)
						claims := rules[1].(map[string]any)["claims_equal"].(map[string]any)
						if claims["email_verified"] != true {
							return fmt.Errorf("email_verified sent as %#v, want the boolean true", claims["email_verified"])
						}
						return nil
					},
				),
			},
			{
				// Removing optional settings clears them rather than leaving the old values.
				Config: app("Admin console", origin, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("phasetwo_gateway_app.admin", "name", "Admin console"),
					resource.TestCheckResourceAttr("phasetwo_gateway_app.admin", "header_mappings.%", "0"),
					resource.TestCheckResourceAttr("phasetwo_gateway_app.admin", "policy.default", "authenticated"),
					resource.TestCheckResourceAttr("phasetwo_gateway_app.admin", "policy.rules.#", "0"),
					resource.TestCheckNoResourceAttr("phasetwo_gateway_app.admin", "post_logout_redirect_uri"),
					resource.TestCheckResourceAttr("phasetwo_gateway_app.admin", "origin_secret", "p2os_test"),
					func(*terraform.State) error {
						a := fake.app("app-1")
						if _, ok := a["post_logout_redirect_uri"]; ok || len(a["header_mappings"].(map[string]any)) != 0 {
							return fmt.Errorf("settings not cleared on the server: %v", a)
						}
						return nil
					},
				),
			},
			{
				PreConfig: func() {
					fake.mu.Lock()
					fake.notReadyReads = 2
					fake.mu.Unlock()
				},
				Config: app("Admin console", origin, "") + activation,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("phasetwo_gateway_app_activation.admin", "verification_result", "VERIFIED"),
					func(*terraform.State) error {
						fake.mu.Lock()
						defer fake.mu.Unlock()
						if s := fake.apps["app-1"]["status"]; s != "ACTIVE" {
							return fmt.Errorf("status %v, want ACTIVE", s)
						}
						// It verified, then polled until the hostname was ready.
						calls := strings.Join(fake.calls, "\n")
						if !regexp.MustCompile(`POST apps/app-1/verify(\nGET apps/app-1){2,}\nPOST apps/app-1/activate`).MatchString(calls) {
							return fmt.Errorf("expected verify, then polling, then activate:\n%s", calls)
						}
						return nil
					},
				),
			},
			{
				Config:      app("Admin console", "https://elsewhere.example.com", "") + activation,
				ExpectError: regexp.MustCompile(`An active app cannot move its\s+origin`),
			},
			{
				ResourceName:            "phasetwo_gateway_app.admin",
				ImportState:             true,
				ImportStateIdFunc:       func(*terraform.State) (string, error) { return "realm-1/app-1", nil },
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"origin_secret", "status"},
				Config:                  app("Admin console", origin, "") + activation,
			},
			{
				// Deactivating out of band drops the activation, so the next apply restores it.
				PreConfig: func() {
					fake.mu.Lock()
					fake.apps["app-1"]["status"] = "DISABLED"
					fake.mu.Unlock()
				},
				Config: app("Admin console", origin, "") + activation,
				Check: func(*terraform.State) error {
					if s := fake.app("app-1")["status"]; s != "ACTIVE" {
						return fmt.Errorf("status %v, want ACTIVE", s)
					}
					return nil
				},
			},
		},
	})
}

func TestGatewayAppActivationRefusesAnOpenOrigin(t *testing.T) {
	gatewayPollInterval = 10 * time.Millisecond
	fake := newFakeGateway()
	fake.verifyResult = "FAILED"
	srv := httptest.NewServer(fake)
	defer srv.Close()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: gatewayProviderConfig(srv.URL) + `
resource "phasetwo_gateway_app" "open" {
  realm_id        = "realm-1"
  name            = "Open"
  public_hostname = "open.example.com"
  origin_url      = "https://open-origin.example.com"
}

resource "phasetwo_gateway_app_activation" "open" {
  realm_id = phasetwo_gateway_app.open.realm_id
  app_id   = phasetwo_gateway_app.open.id
}
`,
			ExpectError: regexp.MustCompile(`origin admits requests without the secret`),
		}},
	})
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, c := range fake.calls {
		if strings.HasSuffix(c, "/activate") {
			t.Fatalf("activated an app whose origin failed verification: %v", fake.calls)
		}
	}
}
