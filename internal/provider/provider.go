// Package provider implements the Phase Two Terraform provider.
package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/p2-inc/terraform-provider-phasetwo/internal/api"
)

// Ensure the implementation satisfies the framework interfaces.
var (
	_ provider.Provider                       = (*phasetwoProvider)(nil)
	_ provider.ProviderWithEphemeralResources = (*phasetwoProvider)(nil)
)

type phasetwoProvider struct {
	version string
}

// New returns a provider constructor for the given build version.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &phasetwoProvider{version: version}
	}
}

type providerModel struct {
	Environment  types.String `tfsdk:"environment"`
	BaseURL      types.String `tfsdk:"base_url"`
	Realm        types.String `tfsdk:"realm"`
	ClientID     types.String `tfsdk:"client_id"`
	ClientSecret types.String `tfsdk:"client_secret"`
	AccessToken  types.String `tfsdk:"access_token"`
}

func (p *phasetwoProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "phasetwo"
	resp.Version = p.version
}

func (p *phasetwoProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manage [Phase Two](https://phasetwo.io) hosted Keycloak clusters and " +
			"the realms running on them.\n\n" +
			"Authenticate with an API secret — an OIDC client-credentials client created for your " +
			"organization in the Phase Two console. Every argument can also be supplied by " +
			"environment variable, which is the recommended way to keep credentials out of " +
			"configuration files.",
		Attributes: map[string]schema.Attribute{
			"environment": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Which hosted Phase Two console to talk to: `app` (production, " +
					"the default) or `app-staging`. Ignored when `base_url` is set. May also be set " +
					"with `PHASETWO_ENVIRONMENT`.",
				Validators: []validator.String{
					stringvalidator.OneOf(api.Environments...),
				},
			},
			"base_url": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Full Keycloak auth root, for self-hosted or local development — " +
					"for example `http://localhost:8080/auth`. This is the auth root, not the API " +
					"base: the realm and API path are appended to it. Overrides `environment`. May " +
					"also be set with `PHASETWO_BASE_URL`.",
			},
			"realm": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Realm the Phase Two console runs in. Defaults to `self`, which " +
					"is correct for the hosted service. May also be set with `PHASETWO_REALM`.",
			},
			"client_id": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Client ID of an API secret. May also be set with " +
					"`PHASETWO_CLIENT_ID`.",
			},
			"client_secret": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				MarkdownDescription: "Client secret of an API secret. May also be set with " +
					"`PHASETWO_CLIENT_SECRET`.",
			},
			"access_token": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				MarkdownDescription: "A bearer token to use directly, instead of exchanging client " +
					"credentials for one. Intended for CI that already holds a token; the provider " +
					"will not refresh it. May also be set with `PHASETWO_ACCESS_TOKEN`.",
			},
		},
	}
}

func (p *phasetwoProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Unknown values mean another resource has to be applied first. Say so rather than reading
	// through to the environment and building a client against a half-known configuration.
	for _, u := range []struct {
		p path.Path
		v types.String
	}{
		{path.Root("environment"), cfg.Environment},
		{path.Root("base_url"), cfg.BaseURL},
		{path.Root("realm"), cfg.Realm},
		{path.Root("client_id"), cfg.ClientID},
		{path.Root("client_secret"), cfg.ClientSecret},
		{path.Root("access_token"), cfg.AccessToken},
	} {
		if u.v.IsUnknown() {
			resp.Diagnostics.AddAttributeError(u.p, "Unknown provider configuration",
				"This value is not known until apply. Move it to a variable or an environment "+
					"variable so the provider can be configured before other resources are planned.")
		}
	}
	if resp.Diagnostics.HasError() {
		return
	}

	c := api.Config{
		Environment:  firstNonEmpty(cfg.Environment.ValueString(), os.Getenv("PHASETWO_ENVIRONMENT")),
		BaseURL:      firstNonEmpty(cfg.BaseURL.ValueString(), os.Getenv("PHASETWO_BASE_URL")),
		Realm:        firstNonEmpty(cfg.Realm.ValueString(), os.Getenv("PHASETWO_REALM")),
		ClientID:     firstNonEmpty(cfg.ClientID.ValueString(), os.Getenv("PHASETWO_CLIENT_ID")),
		ClientSecret: firstNonEmpty(cfg.ClientSecret.ValueString(), os.Getenv("PHASETWO_CLIENT_SECRET")),
		AccessToken:  firstNonEmpty(cfg.AccessToken.ValueString(), os.Getenv("PHASETWO_ACCESS_TOKEN")),
		UserAgent:    "terraform-provider-phasetwo/" + p.version,
	}

	if c.AccessToken == "" && (c.ClientID == "" || c.ClientSecret == "") {
		resp.Diagnostics.AddError(
			"Missing Phase Two API credentials",
			"Set client_id and client_secret (or PHASETWO_CLIENT_ID and PHASETWO_CLIENT_SECRET), "+
				"or supply access_token (or PHASETWO_ACCESS_TOKEN).\n\n"+
				"Create an API secret for your organization in the Phase Two console, or with the "+
				"org.apiSecret.create API operation. The client secret is shown only once.",
		)
		return
	}

	// api.New bakes this context into the client-credentials token source it builds, which is
	// then reused for every future token refresh over the life of the provider instance. The
	// context Configure receives is only valid for this call — it is canceled once Configure
	// returns — so using it here would cancel the first token refresh any resource or data
	// source triggers afterwards. Use a long-lived context instead.
	cl, err := api.New(context.Background(), c)
	if err != nil {
		resp.Diagnostics.AddError("Could not configure the Phase Two client", err.Error())
		return
	}

	resp.DataSourceData = cl
	resp.ResourceData = cl
}

func (p *phasetwoProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewClusterResource,
		NewRealmResource,
		NewClusterDomainResource,
		NewClusterPrimaryHostResource,
		NewClusterIPRulesResource,
		NewEnvironmentVariableResource,
		NewExtensionResource,
		NewExtensionVersionResource,
		NewRealmCredentialResource,
	}
}

// EphemeralResources returns values that are read at apply time and never written to state.
func (p *phasetwoProvider) EphemeralResources(_ context.Context) []func() ephemeral.EphemeralResource {
	return []func() ephemeral.EphemeralResource{
		NewRealmCredentialSecretEphemeralResource,
	}
}

func (p *phasetwoProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewOrganizationDataSource,
		NewOrganizationsDataSource,
		NewPaymentMethodDataSource,
		NewClusterDataSource,
		NewRealmDataSource,
		NewRegionsDataSource,
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
