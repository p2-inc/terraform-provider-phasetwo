package provider

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/p2-inc/terraform-provider-phasetwo/internal/api"
)

var (
	_ ephemeral.EphemeralResource              = (*realmCredentialSecretEphemeral)(nil)
	_ ephemeral.EphemeralResourceWithConfigure = (*realmCredentialSecretEphemeral)(nil)
)

// NewRealmCredentialSecretEphemeralResource returns the phasetwo_realm_credential_secret
// ephemeral resource.
func NewRealmCredentialSecretEphemeralResource() ephemeral.EphemeralResource {
	return &realmCredentialSecretEphemeral{}
}

type realmCredentialSecretEphemeral struct {
	client *api.Client
}

type realmCredentialSecretModel struct {
	RealmID      types.String `tfsdk:"realm_id"`
	ClientID     types.String `tfsdk:"client_id"`
	ClientSecret types.String `tfsdk:"client_secret"`
	ServerURL    types.String `tfsdk:"server_url"`
	Realm        types.String `tfsdk:"realm"`
}

func (e *realmCredentialSecretEphemeral) Metadata(_ context.Context, req ephemeral.MetadataRequest, resp *ephemeral.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_realm_credential_secret"
}

func (e *realmCredentialSecretEphemeral) Schema(_ context.Context, _ ephemeral.SchemaRequest, resp *ephemeral.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The client secret of a " +
			"[`phasetwo_realm_credential`](../resources/realm_credential), read from the realm at " +
			"apply time.\n\n" +
			"Ephemeral resources are never written to state or plan files, so the secret exists " +
			"only for the duration of the run. That is the point: the alternative is exposing it " +
			"as a resource attribute, which puts a `realm-admin` credential into " +
			"`terraform.tfstate` — commonly an unencrypted file in an object store.\n\n" +
			"Phase Two keeps no copy of the secret. This reads it from your Keycloak, which is " +
			"where it has always lived, and reading does not rotate it — so fetching it on every " +
			"apply does not invalidate the credential being used.\n\n" +
			"-> Requires Terraform 1.10 or later.",
		Attributes: map[string]schema.Attribute{
			"realm_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "ID of the realm (deployment) the credential belongs to.",
			},
			"client_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Client ID of the credential to read.",
			},
			"client_secret": schema.StringAttribute{
				Computed:            true,
				Sensitive:           true,
				MarkdownDescription: "The credential's client secret.",
			},
			"server_url": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Base URL of the realm's Keycloak instance.",
			},
			"realm": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Name of the realm this credential administers.",
			},
		},
	}
}

func (e *realmCredentialSecretEphemeral) Configure(_ context.Context, req ephemeral.ConfigureRequest, resp *ephemeral.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*api.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected provider data",
			"Expected *api.Client. Please report this to the provider developers.",
		)
		return
	}
	e.client = c
}

// Open reads the secret. There is no Renew or Close: the value has no server-side lease to keep
// alive and nothing to revoke at the end of a run — it is the credential's standing secret, not a
// short-lived token minted for this apply.
func (e *realmCredentialSecretEphemeral) Open(ctx context.Context, req ephemeral.OpenRequest, resp *ephemeral.OpenResponse) {
	var config realmCredentialSecretModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	credential, err := e.client.ReadCredentialSecret(ctx,
		config.RealmID.ValueString(), config.ClientID.ValueString())
	if err != nil {
		if errors.Is(err, api.ErrCredentialNotFound) {
			// Worth its own message: the usual cause is the credential having been revoked, and
			// "not found" alone sends people looking at their realm_id.
			resp.Diagnostics.AddError(
				"Realm credential not found",
				"No credential "+config.ClientID.ValueString()+" on realm "+
					config.RealmID.ValueString()+". It may have been revoked — through "+
					"`phasetwo_realm_credential`, the API, or by deleting the client in the "+
					"Keycloak console.",
			)
			return
		}
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not read the realm credential's secret", err))
		return
	}

	if credential.ClientSecret == nil || *credential.ClientSecret == "" {
		resp.Diagnostics.AddError(
			"Realm credential has no secret",
			"The API returned credential "+config.ClientID.ValueString()+" without a secret. "+
				"A public client has none; this credential should be confidential.",
		)
		return
	}

	config.ClientSecret = types.StringValue(*credential.ClientSecret)
	config.ServerURL = types.StringValue(credential.ServerUrl)
	config.Realm = types.StringValue(credential.Realm)

	resp.Diagnostics.Append(resp.Result.Set(ctx, &config)...)
}
