// Package api wraps the generated OpenAPI client with the behaviour the provider needs on
// top of plain HTTP: OAuth2 client-credentials auth, typed errors, and serialization of the
// config changes that restart a cluster.
package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/p2-inc/terraform-provider-phasetwo/internal/client"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

// DefaultEnvironment is the production console. The spec's `servers` block templates this as
// `{environment}` in `https://{environment}.phasetwo.io/auth/realms/{realm}/v2`.
const DefaultEnvironment = "app"

// DefaultRealm is the realm the console itself runs in. Every published operation lives under it.
const DefaultRealm = "self"

// Environments are the values the spec's `environment` server variable allows.
var Environments = []string{"app", "app-staging"}

// Config is everything needed to reach the API. Either ClientID+ClientSecret or AccessToken must
// be set; the former is the documented path and the latter exists for CI that already holds a
// token.
type Config struct {
	// Environment selects a hosted console ("app" or "app-staging"). Ignored when BaseURL is set.
	Environment string
	// BaseURL overrides Environment entirely, for self-hosted or local development. It is the
	// Keycloak *auth root* — e.g. "https://localhost:8080/auth" — not the API base; the realm and
	// the `v2` provider path are appended to it.
	BaseURL string
	Realm   string

	ClientID     string
	ClientSecret string
	AccessToken  string

	HTTPClient *http.Client
	UserAgent  string
}

// authRoot returns the Keycloak base that both the API path and the token endpoint hang off.
func (c Config) authRoot() string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	env := c.Environment
	if env == "" {
		env = DefaultEnvironment
	}
	return fmt.Sprintf("https://%s.phasetwo.io/auth", env)
}

func (c Config) realm() string {
	if c.Realm == "" {
		return DefaultRealm
	}
	return c.Realm
}

// APIBase is the server URL from the spec. The trailing `v2` is the id of the Keycloak realm
// resource provider that serves this API; without it requests reach the legacy `clusters`
// provider, which is a different API.
func (c Config) APIBase() string {
	return fmt.Sprintf("%s/realms/%s/v2", c.authRoot(), c.realm())
}

// TokenURL is the OIDC token endpoint for the client-credentials grant.
func (c Config) TokenURL() string {
	return fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token", c.authRoot(), c.realm())
}

// Client is the provider's handle on the API.
type Client struct {
	api *client.ClientWithResponses
	cfg Config

	restarts *restartGate
}

// New builds a Client. It does not contact the network: with client credentials the token is
// fetched lazily on the first call, so a bad secret surfaces as a failed API call rather than a
// failed provider configuration.
//
// ctx is retained for the life of the returned Client and reused for every future token refresh,
// so it must outlive this call — never pass a context scoped to a single RPC (such as a
// terraform-plugin-framework Configure context, which is canceled as soon as Configure returns).
func New(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.AccessToken == "" && (cfg.ClientID == "" || cfg.ClientSecret == "") {
		return nil, errors.New("either access_token or both client_id and client_secret must be set")
	}

	base := cfg.HTTPClient
	if base == nil {
		base = &http.Client{Timeout: 60 * time.Second}
	}

	var httpClient *http.Client
	if cfg.AccessToken != "" {
		ctx := context.WithValue(ctx, oauth2.HTTPClient, base)
		src := oauth2.StaticTokenSource(&oauth2.Token{
			AccessToken: cfg.AccessToken,
			TokenType:   "Bearer",
		})
		httpClient = oauth2.NewClient(ctx, src)
	} else {
		ccfg := &clientcredentials.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			TokenURL:     cfg.TokenURL(),
			AuthStyle:    oauth2.AuthStyleInParams,
		}
		ctx := context.WithValue(ctx, oauth2.HTTPClient, base)
		httpClient = ccfg.Client(ctx)
	}

	opts := []client.ClientOption{client.WithHTTPClient(httpClient)}
	if cfg.UserAgent != "" {
		ua := cfg.UserAgent
		opts = append(opts, client.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
			req.Header.Set("User-Agent", ua)
			return nil
		}))
	}

	api, err := client.NewClientWithResponses(cfg.APIBase(), opts...)
	if err != nil {
		return nil, fmt.Errorf("building API client: %w", err)
	}

	return &Client{api: api, cfg: cfg, restarts: newRestartGate()}, nil
}

// API exposes the generated client. Resources use this directly for calls that need no wrapping.
func (c *Client) API() *client.ClientWithResponses { return c.api }

// Config returns the configuration the client was built with.
func (c *Client) Config() Config { return c.cfg }

// Error is a non-2xx API response.
type Error struct {
	StatusCode int
	Status     string
	// Message is the server's error text when it sent one, else the raw body, truncated.
	Message string
	// Op names the operation, for messages that would otherwise be hard to place.
	Op string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("%s: %s", e.Op, e.Status)
	}
	return fmt.Sprintf("%s: %s: %s", e.Op, e.Status, e.Message)
}

// IsNotFound reports whether err is a 404.
func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.StatusCode == http.StatusNotFound
}

// IsConflict reports whether err is a 409. The API uses 409 both for "a restart is already in
// flight" and for tier limits, so callers that retry must check the message too.
func IsConflict(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.StatusCode == http.StatusConflict
}

// asError is errors.As specialised to *Error, so callers avoid repeating the type dance.
func asError(err error, target **Error) bool { return errors.As(err, target) }

// IsUnauthorized reports whether err is a 401 or 403.
func IsUnauthorized(err error) bool {
	var e *Error
	return errors.As(err, &e) &&
		(e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden)
}
