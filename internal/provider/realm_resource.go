package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/p2-inc/terraform-provider-phasetwo/internal/api"
	"github.com/p2-inc/terraform-provider-phasetwo/internal/client"
)

const defaultRealmCreateTimeout = 20 * time.Minute

var (
	_ resource.Resource                = (*realmResource)(nil)
	_ resource.ResourceWithConfigure   = (*realmResource)(nil)
	_ resource.ResourceWithImportState = (*realmResource)(nil)
)

// NewRealmResource returns the phasetwo_realm resource.
func NewRealmResource() resource.Resource { return &realmResource{} }

type realmResource struct {
	client *api.Client
}

type realmModel struct {
	ID          types.String `tfsdk:"id"`
	ClusterID   types.String `tfsdk:"cluster_id"`
	Name        types.String `tfsdk:"name"`
	DisplayName types.String `tfsdk:"display_name"`

	State           types.String `tfsdk:"state"`
	StateError      types.String `tfsdk:"state_error"`
	Region          types.String `tfsdk:"region"`
	OrganizationID  types.String `tfsdk:"organization_id"`
	CreatedByUserID types.String `tfsdk:"created_by_user_id"`
	CreatedAt       types.String `tfsdk:"created_at"`

	Timeouts timeouts.Value `tfsdk:"timeouts"`
}

func (r *realmResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_realm"
}

func (r *realmResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A realm on a dedicated Phase Two cluster — a *deployment* in the " +
			"API's terms.\n\n" +
			"This creates an empty realm. To configure what is inside it (clients, identity " +
			"providers, flows), point a Keycloak provider at the cluster's host.\n\n" +
			"The number of realms a cluster may hold depends on its tier: 5 on `starter`, 20 on " +
			"`premium`, 100 on `enterprise`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Unique identifier of the realm.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"cluster_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "ID of the cluster to create the realm on. The cluster must be `ACTIVE`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Realm name, which is also its Keycloak realm name. Lowercased " +
					"by the API, so a name with capitals will come back changed.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"display_name": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Human-readable name. This is the only attribute that can be " +
					"changed in place; everything else forces replacement.",
			},

			"state": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Lifecycle state: `PENDING`, `ACTIVE`, `DISABLED` or `FAILED`.",
			},
			"state_error": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Why the realm last failed, when it is in `FAILED`.",
			},
			"region": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Region of the cluster hosting this realm.",
			},
			"organization_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "ID of the organization that owns the realm.",
			},
			"created_by_user_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "ID of the user who created the realm.",
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "When the realm was created, as an RFC 3339 timestamp.",
			},

			"timeouts": timeouts.AttributesAll(ctx),
		},
	}
}

func (r *realmResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

func (r *realmResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan realmModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	configured, diags := plan.Timeouts.Create(ctx, defaultRealmCreateTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout := defaultTimeout(configured, defaultRealmCreateTimeout)
	ctx, cancel := ctxWithTimeout(ctx, timeout)
	defer cancel()

	clusterID := plan.ClusterID.ValueString()
	id, err := r.client.CreateRealm(ctx, clusterID, plan.Name.ValueString())
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not create the realm", err))
		return
	}

	// Persist the id before the wait, so an interrupted apply does not orphan the realm. The
	// computed attributes are still unknown at this point and state cannot hold unknowns, so
	// they go in as null.
	partial := plan
	partial.ID = types.StringValue(id)
	partial.markComputedUnset()
	resp.State.Set(ctx, &partial)

	plan.ID = types.StringValue(id)

	realm, err := r.client.WaitForRealmActive(ctx, id, timeout)
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("Realm did not become active", err))
		return
	}

	// display_name is not part of the create request, so set it separately when asked for.
	if !plan.DisplayName.IsNull() && plan.DisplayName.ValueString() != "" {
		if err := r.client.UpdateRealm(ctx, id, plan.DisplayName.ValueString()); err != nil {
			resp.Diagnostics.Append(apiErrorDiagnostic("Could not set the realm's display name", err))
			return
		}
		if realm, err = r.client.GetRealm(ctx, id); err != nil {
			resp.Diagnostics.Append(apiErrorDiagnostic("Could not re-read the realm", err))
			return
		}
	}

	r.apply(&plan, realm)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *realmResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state realmModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	realm, err := r.client.GetRealm(ctx, state.ID.ValueString())
	if err != nil {
		if api.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not read the realm", err))
		return
	}

	r.apply(&state, realm)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *realmResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state realmModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()
	if err := r.client.UpdateRealm(ctx, id, plan.DisplayName.ValueString()); err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not update the realm", err))
		return
	}

	realm, err := r.client.GetRealm(ctx, id)
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not re-read the realm after updating it", err))
		return
	}

	r.apply(&plan, realm)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *realmResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state realmModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteRealm(ctx, state.ID.ValueString()); err != nil {
		if api.IsNotFound(err) {
			return
		}
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not delete the realm", err))
	}
}

// ImportState accepts either a bare realm id or `cluster_id/realm_id`. The API can resolve a
// realm from its id alone, so the bare form is enough; the compound form is accepted because it
// reads more naturally and matches how the resource is written.
func (r *realmResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := req.ID
	if parts := strings.SplitN(req.ID, "/", 2); len(parts) == 2 {
		if parts[0] == "" || parts[1] == "" {
			resp.Diagnostics.AddError("Invalid import ID",
				fmt.Sprintf("Expected %q or %q, got %q", "<realm_id>", "<cluster_id>/<realm_id>", req.ID))
			return
		}
		id = parts[1]
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

// markComputedUnset nulls every computed attribute; see the note on clusterModel.
func (m *realmModel) markComputedUnset() {
	m.State = types.StringNull()
	m.StateError = types.StringNull()
	m.Region = types.StringNull()
	m.OrganizationID = types.StringNull()
	m.CreatedByUserID = types.StringNull()
	m.CreatedAt = types.StringNull()
}

func (r *realmResource) apply(m *realmModel, d *client.Deployment) {
	m.ID = types.StringValue(d.Id)
	m.Name = types.StringValue(d.Name)
	m.State = types.StringValue(string(d.State))
	m.StateError = stringOrNull(d.StateError)
	m.Region = stringOrNull(d.Region)
	m.OrganizationID = stringOrNull(d.OrgId)
	m.CreatedByUserID = stringOrNull(d.CreatedByUserId)
	m.CreatedAt = epochMillisPtrToRFC3339(d.CreatedAt)

	// The API omits display_name when unset. Leaving the model's value alone in that case keeps
	// a null config attribute null instead of flapping between null and "".
	if d.DisplayName != nil && *d.DisplayName != "" {
		m.DisplayName = types.StringValue(*d.DisplayName)
	}

	if d.Cluster != nil {
		m.ClusterID = types.StringValue(d.Cluster.Id)
	}
}
