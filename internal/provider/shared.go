package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/p2-inc/terraform-provider-phasetwo/internal/api"
)

// configureResourceClient pulls the shared client out of the framework's provider data. It
// returns nil when the provider has not been configured yet, which the framework does on its
// first pass and is not an error.
func configureResourceClient(req resource.ConfigureRequest, resp *resource.ConfigureResponse) *api.Client {
	if req.ProviderData == nil {
		return nil
	}
	c, ok := req.ProviderData.(*api.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data",
			fmt.Sprintf("Expected *api.Client, got %T. This is a bug in the provider.", req.ProviderData))
		return nil
	}
	return c
}

// configureDataSourceClient is configureResourceClient for data sources.
func configureDataSourceClient(req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) *api.Client {
	if req.ProviderData == nil {
		return nil
	}
	c, ok := req.ProviderData.(*api.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data",
			fmt.Sprintf("Expected *api.Client, got %T. This is a bug in the provider.", req.ProviderData))
		return nil
	}
	return c
}

// epochMillisToRFC3339 renders an API timestamp as an RFC 3339 string, which is what Terraform
// practitioners expect to see in state. The API sends epoch milliseconds.
func epochMillisToRFC3339(ms int64) types.String {
	if ms == 0 {
		return types.StringNull()
	}
	return types.StringValue(time.UnixMilli(ms).UTC().Format(time.RFC3339))
}

// epochMillisPtrToRFC3339 is epochMillisToRFC3339 for an optional timestamp.
func epochMillisPtrToRFC3339(ms *int64) types.String {
	if ms == nil {
		return types.StringNull()
	}
	return epochMillisToRFC3339(*ms)
}

// stringOrNull converts an optional API string to a Terraform value.
func stringOrNull(s *string) types.String {
	if s == nil {
		return types.StringNull()
	}
	return types.StringValue(*s)
}

// apiErrorDiagnostic renders an API error, adding guidance for the cases where the raw message
// is not enough to act on.
func apiErrorDiagnostic(summary string, err error) diag.Diagnostic {
	detail := err.Error()
	switch {
	case api.IsUnauthorized(err):
		detail += "\n\nThe API secret may lack the roles this operation needs, or may have been " +
			"deleted. API secrets are per-organization; check the token is for the organization " +
			"that owns this resource."
	case api.IsConflict(err):
		detail += "\n\nA 409 usually means either a name is already taken or the cluster's tier " +
			"limit has been reached. Tier limits (realms / themes / extensions / domains / IP " +
			"rules) are: starter 5/1/0/2/0, premium 20/1/1/5/2, enterprise 100/∞/∞/15/∞."
	}
	return diag.NewErrorDiagnostic(summary, detail)
}

// defaultTimeout resolves a timeout, falling back when the practitioner did not set one.
func defaultTimeout(configured time.Duration, fallback time.Duration) time.Duration {
	if configured <= 0 {
		return fallback
	}
	return configured
}

// ctxWithTimeout is a small helper so resources do not repeat the cancel dance.
func ctxWithTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}
