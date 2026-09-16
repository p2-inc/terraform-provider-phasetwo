package provider

import (
	"context"
	"errors"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/p2-inc/terraform-provider-phasetwo/internal/api"
)

var (
	_ resource.Resource                = (*realmCredentialResource)(nil)
	_ resource.ResourceWithConfigure   = (*realmCredentialResource)(nil)
	_ resource.ResourceWithImportState = (*realmCredentialResource)(nil)
)

// NewRealmCredentialResource returns the phasetwo_realm_credential resource.
func NewRealmCredentialResource() resource.Resource { return &realmCredentialResource{} }

type realmCredentialResource struct {
	client *api.Client
}

type realmCredentialModel struct {
	ID          types.String `tfsdk:"id"`
	RealmID     types.String `tfsdk:"realm_id"`
	ClientID    types.String `tfsdk:"client_id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Roles       types.List   `tfsdk:"roles"`
	ServerURL   types.String `tfsdk:"server_url"`
	Realm       types.String `tfsdk:"realm"`
}

func (r *realmCredentialResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_realm_credential"
}

func (r *realmCredentialResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A service account client on a realm, for administering that realm " +
			"directly — with the [Keycloak provider]" +
			"(https://registry.terraform.io/providers/keycloak/keycloak/latest/docs), a " +
			"provisioning script, or anything else that speaks Keycloak's admin API.\n\n" +
			"Separate from the client Phase Two uses to administer the same realm. This one is " +
			"yours to hold and to revoke, and a leak of it has no bearing on the other.\n\n" +
			"~> **There is deliberately no `client_secret` attribute.** Terraform writes every " +
			"attribute to state, so exposing it here would put a `realm-admin` credential into " +
			"`terraform.tfstate` — commonly an unencrypted file in an object store. Read the " +
			"secret through the " +
			"[`phasetwo_realm_credential_secret`](../ephemeral-resources/realm_credential_secret) " +
			"ephemeral resource instead, which is never persisted to state or plan files.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Same as `client_id`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"realm_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "ID of the realm (deployment) this credential administers.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"client_id": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Client ID to authenticate with, generated as " +
					"`api-{name}-{random}`. Not predictable from `name`, so it is only known " +
					"after apply.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Short name folded into the generated client ID so the " +
					"credential is recognisable in the realm's client list, e.g. `terraform` " +
					"produces `api-terraform-9f3c1a2b`. Lower-case letters, digits and hyphens, " +
					"up to 48 characters.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"description": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Note recorded on the client, to tell credentials apart " +
					"later. Up to 255 characters.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"roles": schema.ListAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				MarkdownDescription: "`realm-management` roles to grant, e.g. " +
					"`[\"view-users\", \"view-realm\"]`. Defaults to `[\"realm-admin\"]`, which " +
					"is full administrative access to the realm and what the Keycloak provider " +
					"generally needs.\n\n" +
					"Narrow it where you can. Note that a credential without a role Terraform " +
					"needs fails partway through an apply with a 403, after some resources have " +
					"already been created — work the set out on a throwaway realm first.",
				PlanModifiers: []planmodifier.List{listplanmodifier.RequiresReplace()},
			},
			"server_url": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Base URL of the realm's Keycloak instance.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"realm": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Name of the realm this credential administers.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *realmCredentialResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

// Create mints the credential. The secret comes back in this response and is deliberately dropped
// on the floor — keeping it would mean writing it to state, which is the thing this resource
// exists to avoid.
func (r *realmCredentialResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan realmCredentialModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var roles []string
	if !plan.Roles.IsNull() && !plan.Roles.IsUnknown() {
		resp.Diagnostics.Append(plan.Roles.ElementsAs(ctx, &roles, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	credential, err := r.client.CreateCredential(
		ctx,
		plan.RealmID.ValueString(),
		plan.Name.ValueString(),
		plan.Description.ValueString(),
		roles,
	)
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not create the realm credential", err))
		return
	}

	plan.ID = types.StringValue(credential.ClientId)
	plan.ClientID = types.StringValue(credential.ClientId)
	plan.ServerURL = types.StringValue(credential.ServerUrl)
	plan.Realm = types.StringValue(credential.Realm)
	rolesList, diags := types.ListValueFrom(ctx, types.StringType, credential.Roles)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.Roles = rolesList

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read confirms the credential still exists and refreshes its roles.
//
// It deliberately does not fetch the secret. Read runs on every plan, and pulling a realm-admin
// secret through the provider on every plan — to then discard it — would be all cost and no
// benefit.
func (r *realmCredentialResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state realmCredentialModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	credential, err := r.client.GetCredential(ctx,
		state.RealmID.ValueString(), state.ClientID.ValueString())
	if err != nil {
		// Revoked out of band, or the realm is gone. Either way Terraform should plan to recreate
		// it rather than fail: a credential somebody revoked by hand is exactly the case where
		// re-running apply should put things right.
		if errors.Is(err, api.ErrCredentialNotFound) || api.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not read the realm credential", err))
		return
	}

	state.ID = types.StringValue(credential.ClientId)
	state.ClientID = types.StringValue(credential.ClientId)
	state.ServerURL = types.StringValue(credential.ServerUrl)
	state.Realm = types.StringValue(credential.Realm)
	if credential.Description != nil && *credential.Description != "" {
		state.Description = types.StringValue(*credential.Description)
	}
	if credential.Name != nil && *credential.Name != "" {
		state.Name = types.StringValue(*credential.Name)
	}
	rolesList, diags := types.ListValueFrom(ctx, types.StringType, credential.Roles)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.Roles = rolesList

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is unreachable: every argument forces replacement, because none of them can be changed on
// an existing credential through the API. Terraform still requires the method to exist.
func (r *realmCredentialResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError(
		"Realm credentials cannot be updated in place",
		"Every argument of phasetwo_realm_credential forces replacement, so this should not be "+
			"reachable. Please report it.",
	)
}

func (r *realmCredentialResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state realmCredentialModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.DeleteCredential(ctx,
		state.RealmID.ValueString(), state.ClientID.ValueString())
	if err != nil && !api.IsNotFound(err) {
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not revoke the realm credential", err))
	}
}

// ImportState takes `realm_id/client_id`, since a credential is only addressable within its realm.
func (r *realmCredentialResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	realmID, clientID, ok := strings.Cut(req.ID, "/")
	if !ok || realmID == "" || clientID == "" {
		resp.Diagnostics.AddError(
			"Unexpected import identifier",
			"Expected `realm_id/client_id`, for example "+
				"`3f9a1c2e-.../api-terraform-9f3c1a2b`, got: "+req.ID,
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("realm_id"), realmID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("client_id"), clientID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), clientID)...)
}
