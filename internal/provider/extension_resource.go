package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/p2-inc/terraform-provider-phasetwo/internal/api"
	"github.com/p2-inc/terraform-provider-phasetwo/internal/client"
)

var (
	_ resource.Resource                = (*extensionResource)(nil)
	_ resource.ResourceWithConfigure   = (*extensionResource)(nil)
	_ resource.ResourceWithImportState = (*extensionResource)(nil)
)

// NewExtensionResource returns the phasetwo_cluster_extension resource.
func NewExtensionResource() resource.Resource { return &extensionResource{} }

type extensionResource struct {
	client *api.Client
}

type extensionModel struct {
	ID           types.String `tfsdk:"id"`
	ClusterID    types.String `tfsdk:"cluster_id"`
	Name         types.String `tfsdk:"name"`
	ResourceType types.String `tfsdk:"resource_type"`
	Enabled      types.Bool   `tfsdk:"enabled"`
	CreatedAt    types.String `tfsdk:"created_at"`
}

func (r *extensionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cluster_extension"
}

func (r *extensionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An extension slot on a dedicated cluster — a custom provider, theme " +
			"or password blacklist.\n\n" +
			"This registers the slot only. Nothing is deployed until you attach a " +
			"`phasetwo_cluster_extension_version` carrying the actual file, which is also what " +
			"triggers the cluster reconcile.\n\n" +
			"How many extensions a cluster may hold depends on its tier: `starter` allows one " +
			"theme and no extensions, `premium` one of each, `enterprise` unlimited.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Unique identifier of the extension.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"cluster_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "ID of the cluster.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Extension name, unique within the cluster.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"resource_type": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "What kind of resource this holds:\n\n" +
					"- `EXTENSION` — a Keycloak provider jar\n" +
					"- `THEME` — a theme jar\n" +
					"- `PASSWORD_BLACKLIST` — a blacklist file\n\n" +
					"`EXTENSION` and `THEME` are tied to a Keycloak major version, so their " +
					"versions need `keycloak_major_version`. `PASSWORD_BLACKLIST` is " +
					"version-independent.",
				Validators: []validator.String{
					stringvalidator.OneOf(
						string(api.ExtExtension),
						string(api.ExtTheme),
						string(api.ExtPasswordBlacklist),
					),
				},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"enabled": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(true),
				MarkdownDescription: "Whether the extension is active. Disabling does not delete " +
					"its versions.",
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "When the extension was created, as an RFC 3339 timestamp.",
			},
		},
	}
}

func (r *extensionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

func (r *extensionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan extensionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	clusterID := plan.ClusterID.ValueString()
	rt := client.ResourceType(plan.ResourceType.ValueString())

	id, err := r.client.CreateExtension(ctx, clusterID, plan.Name.ValueString(), rt)
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not create the extension", err))
		return
	}

	// Record the id before the follow-up calls. created_at is still unknown here and state
	// cannot hold unknowns, so it goes in as null; see the note on clusterModel.
	partial := plan
	partial.ID = types.StringValue(id)
	partial.CreatedAt = types.StringNull()
	resp.State.Set(ctx, &partial)

	plan.ID = types.StringValue(id)

	// The API creates extensions enabled. Only call the toggle when that is not what was asked
	// for, to avoid a pointless write.
	if !plan.Enabled.ValueBool() {
		if _, err := r.client.SetExtensionEnabled(ctx, clusterID, id, false); err != nil {
			resp.Diagnostics.Append(apiErrorDiagnostic("Could not disable the extension", err))
			return
		}
	}

	ext, err := r.client.GetExtension(ctx, clusterID, id)
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not re-read the extension", err))
		return
	}
	r.apply(&plan, ext)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *extensionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state extensionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	ext, err := r.client.GetExtension(ctx, state.ClusterID.ValueString(), state.ID.ValueString())
	if err != nil {
		if api.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not read the extension", err))
		return
	}

	r.apply(&state, ext)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update handles `enabled`; everything else forces replacement.
func (r *extensionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state extensionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	plan.ID = state.ID
	ext, err := r.client.SetExtensionEnabled(ctx, plan.ClusterID.ValueString(),
		plan.ID.ValueString(), plan.Enabled.ValueBool())
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not update the extension", err))
		return
	}

	r.apply(&plan, ext)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *extensionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state extensionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	clusterID := state.ClusterID.ValueString()
	err := r.client.DeleteExtension(ctx, clusterID, state.ID.ValueString())
	if err != nil && !api.IsNotFound(err) {
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not delete the extension", err))
		return
	}

	// Removing the record is not enough: the running deployment keeps the old artifact until the
	// cluster reconciles. Do it here so a destroy actually takes effect.
	if err := r.client.ReconcileExtensions(ctx, clusterID); err != nil && !api.IsNotFound(err) {
		resp.Diagnostics.AddWarning(
			"Extension deleted but the cluster was not reconciled",
			fmt.Sprintf("The extension record is gone, but the running Keycloak deployment still "+
				"has its files until the cluster restarts: %s\n\nApply again, or restart the "+
				"cluster from the Phase Two console.", err))
	}
}

// ImportState takes `cluster_id/extension_id`.
func (r *extensionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("Expected %q, got %q", "<cluster_id>/<extension_id>", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cluster_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
}

func (r *extensionResource) apply(m *extensionModel, e *client.Extension) {
	m.ID = types.StringValue(e.Id)
	m.Name = types.StringValue(e.Name)
	m.Enabled = types.BoolValue(e.Enabled)
	m.ResourceType = types.StringValue(string(e.ResourceType))
	m.CreatedAt = epochMillisPtrToRFC3339(e.CreatedAt)
}
