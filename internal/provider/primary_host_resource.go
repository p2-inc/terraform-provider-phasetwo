package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/p2-inc/terraform-provider-phasetwo/internal/api"
)

var (
	_ resource.Resource                = (*clusterPrimaryHostResource)(nil)
	_ resource.ResourceWithConfigure   = (*clusterPrimaryHostResource)(nil)
	_ resource.ResourceWithImportState = (*clusterPrimaryHostResource)(nil)
)

// NewClusterPrimaryHostResource returns the phasetwo_cluster_primary_host resource.
func NewClusterPrimaryHostResource() resource.Resource { return &clusterPrimaryHostResource{} }

type clusterPrimaryHostResource struct {
	client *api.Client
}

type clusterPrimaryHostModel struct {
	ID        types.String `tfsdk:"id"`
	ClusterID types.String `tfsdk:"cluster_id"`
	Host      types.String `tfsdk:"host"`
}

func (r *clusterPrimaryHostResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cluster_primary_host"
}

func (r *clusterPrimaryHostResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The hostname a dedicated cluster serves on.\n\n" +
			"This is a singleton: a cluster has exactly one primary hostname, so declare at most " +
			"one of these per cluster. It switches between hostnames that already exist — it does " +
			"not provision one. Add a `phasetwo_cluster_domain` first and wait for its certificate " +
			"to be issued.\n\n" +
			"Destroying this resource does not move the cluster back to its default hostname; it " +
			"only stops Terraform managing which host is current.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Same as `cluster_id` — a cluster has only one primary " +
					"hostname, so the cluster identifies the resource.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"cluster_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "ID of the cluster.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"host": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Hostname to serve on. Either the cluster's default " +
					"`<name>.global.auth.ac` address or one of its custom domains whose " +
					"certificate has been issued. The host must already be reachable — the API " +
					"checks before switching.",
			},
		},
	}
}

func (r *clusterPrimaryHostResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

func (r *clusterPrimaryHostResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan clusterPrimaryHostModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(r.set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *clusterPrimaryHostResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state clusterPrimaryHostModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cluster, err := r.client.GetCluster(ctx, state.ClusterID.ValueString())
	if err != nil {
		if api.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not read the cluster", err))
		return
	}

	state.ID = types.StringValue(cluster.Id)
	state.Host = types.StringValue(cluster.Host)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *clusterPrimaryHostResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan clusterPrimaryHostModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(r.set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete is a no-op beyond dropping state. There is no API call that un-sets a primary hostname
// — a cluster always serves on something — so the honest behaviour is to stop tracking it.
func (r *clusterPrimaryHostResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning(
		"Primary hostname left as it is",
		"Removing this resource stops Terraform managing the cluster's primary hostname. The "+
			"cluster keeps serving on whatever host is currently set; there is no API call to "+
			"revert it. Set it explicitly if you need a different one.")
}

func (r *clusterPrimaryHostResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cluster_id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// set applies the host to the cluster and updates the model in place. Create and Update differ
// only in which state object they write to, so they do that themselves.
func (r *clusterPrimaryHostResource) set(ctx context.Context, plan *clusterPrimaryHostModel) diag.Diagnostics {
	var diags diag.Diagnostics

	cluster, err := r.client.SetClusterHost(ctx, plan.ClusterID.ValueString(), plan.Host.ValueString())
	if err != nil {
		summary := "Could not set the cluster's primary hostname"
		if api.IsConflict(err) || api.IsNotFound(err) {
			diags.Append(apiErrorDiagnostic(summary,
				fmt.Errorf("%w\n\nThe host must already be provisioned on this cluster and, for a "+
					"custom domain, have an issued certificate. Check the certificate_status of "+
					"the matching phasetwo_cluster_domain", err)))
			return diags
		}
		diags.Append(apiErrorDiagnostic(summary, err))
		return diags
	}

	plan.ID = types.StringValue(cluster.Id)
	plan.Host = types.StringValue(cluster.Host)
	return diags
}
