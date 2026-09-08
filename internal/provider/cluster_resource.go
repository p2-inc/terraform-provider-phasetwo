package provider

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/p2-inc/terraform-provider-phasetwo/internal/api"
	"github.com/p2-inc/terraform-provider-phasetwo/internal/client"
)

const (
	defaultClusterCreateTimeout = 45 * time.Minute
	defaultClusterDeleteTimeout = 15 * time.Minute
)

var (
	_ resource.Resource                = (*clusterResource)(nil)
	_ resource.ResourceWithConfigure   = (*clusterResource)(nil)
	_ resource.ResourceWithImportState = (*clusterResource)(nil)
)

// NewClusterResource returns the phasetwo_cluster resource.
func NewClusterResource() resource.Resource { return &clusterResource{} }

type clusterResource struct {
	client *api.Client
}

type clusterModel struct {
	ID              types.String `tfsdk:"id"`
	Name            types.String `tfsdk:"name"`
	Region          types.String `tfsdk:"region"`
	OrganizationID  types.String `tfsdk:"organization_id"`
	PaymentMethodID types.String `tfsdk:"payment_method_id"`
	Tier            types.String `tfsdk:"tier"`
	BillingPeriod   types.String `tfsdk:"billing_period"`
	Domain          types.String `tfsdk:"domain"`

	Host           types.String `tfsdk:"host"`
	Status         types.String `tfsdk:"status"`
	Owner          types.String `tfsdk:"owner"`
	Variant        types.String `tfsdk:"variant"`
	ResourceLimits types.String `tfsdk:"resource_limits"`
	CreatedAt      types.String `tfsdk:"created_at"`

	Timeouts timeouts.Value `tfsdk:"timeouts"`
}

func (r *clusterResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cluster"
}

func (r *clusterResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A dedicated Phase Two Keycloak cluster.\n\n" +
			"Creating a cluster charges the organization's payment method and provisions real " +
			"infrastructure, which usually takes several minutes.\n\n" +
			"~> **Destroying a cluster does not remove it immediately.** Unless it never " +
			"completed billing setup, the cluster moves to `PENDING_DELETION` and is torn down at " +
			"the end of the current billing cycle. It continues to bill until then, and its name " +
			"stays taken — a later cluster cannot reuse it.\n\n" +
			"~> Nothing about a cluster can be changed in place. Every argument forces " +
			"replacement, which means destroying the existing cluster and all realms on it.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Unique identifier of the cluster.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Cluster name, unique across Phase Two. Becomes part of the " +
					"cluster's default hostname.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"region": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "AWS region to provision into, e.g. `US_EAST_1`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"organization_id": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "ID of the organization (team) that will own and be billed for " +
					"the cluster. Organizations are not managed by this provider — look one up with " +
					"the `phasetwo_organization` data source.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"payment_method_id": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "ID of a saved payment method on the organization, which is " +
					"charged for the cluster. Payment methods are not managed by this provider — " +
					"look one up with the `phasetwo_payment_method` data source.\n\n" +
					"The API treats this as optional, but omitting it returns a Stripe Checkout " +
					"link to open in a browser, which Terraform cannot complete. It is therefore " +
					"required here.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"tier": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString("starter"),
				MarkdownDescription: "Subscription tier, which sets the cluster's resource limits. " +
					"One of `starter`, `premium`, `enterprise`. Defaults to `starter`.",
				Validators: []validator.String{
					stringvalidator.OneOf("starter", "premium", "enterprise"),
				},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"billing_period": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("monthly"),
				MarkdownDescription: "Billing period: `monthly` (the default) or `annual`.",
				Validators: []validator.String{
					stringvalidator.OneOf("monthly", "annual"),
				},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"domain": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Custom domain to record at provision time. This is not the " +
					"live hostname — to serve the cluster on a custom domain, add a " +
					"`phasetwo_cluster_domain` and then point `phasetwo_cluster_primary_host` at it.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},

			"host": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Base URL of the cluster's Keycloak instance.",
			},
			"status": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Lifecycle state. `ACTIVE` once provisioned; `PENDING_DELETION` " +
					"and `ARCHIVED` are terminal.",
			},
			"owner": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "ID of the organization that owns the cluster.",
			},
			"variant": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "`DEDICATED` when the cluster has an owning organization.",
			},
			"resource_limits": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "`standard`, or `custom` when the cluster has been exempted " +
					"from its tier's count limits.",
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "When the cluster was created, as an RFC 3339 timestamp.",
			},

			"timeouts": timeouts.AttributesAll(ctx),
		},
	}
}

func (r *clusterResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

func (r *clusterResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan clusterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	configured, diags := plan.Timeouts.Create(ctx, defaultClusterCreateTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout := defaultTimeout(configured, defaultClusterCreateTimeout)
	ctx, cancel := ctxWithTimeout(ctx, timeout)
	defer cancel()

	tier := client.Tier(plan.Tier.ValueString())
	period := client.BillingPeriod(plan.BillingPeriod.ValueString())
	orgID := plan.OrganizationID.ValueString()
	pmID := plan.PaymentMethodID.ValueString()

	body := client.DedicatedClusterRequest{
		Name:            plan.Name.ValueString(),
		Region:          plan.Region.ValueString(),
		OrgId:           orgID,
		PaymentMethodId: &pmID,
		Tier:            &tier,
		BillingPeriod:   &period,
	}
	if !plan.Domain.IsNull() && plan.Domain.ValueString() != "" {
		d := plan.Domain.ValueString()
		body.Domain = &d
	}

	cluster, err := r.client.CreateCluster(ctx, body)
	if err != nil {
		// The 3DS path leaves a real cluster behind. Record its id before failing, or Terraform
		// loses track of a resource that exists and bills.
		var actionErr *api.ErrPaymentActionRequired
		if errors.As(err, &actionErr) && actionErr.CreatedClusterID != "" {
			plan.ID = types.StringValue(actionErr.CreatedClusterID)
			plan.markComputedUnset()
			resp.State.Set(ctx, &plan)
			resp.Diagnostics.AddError(
				"Cluster created but payment needs confirmation",
				fmt.Sprintf("Cluster %s exists but its payment method requires 3-D Secure "+
					"confirmation, which has to be completed in a browser.\n\n"+
					"The cluster id has been written to state so it is not orphaned. Complete "+
					"payment in the Phase Two console and re-run, or destroy this resource.",
					actionErr.CreatedClusterID),
			)
			return
		}
		if errors.Is(err, api.ErrCheckoutRequired) {
			resp.Diagnostics.AddAttributeError(path.Root("payment_method_id"),
				"Cluster creation requires Stripe Checkout",
				"The API returned a Stripe Checkout link rather than a cluster, which means the "+
					"payment method was not accepted. Checkout is a browser flow and cannot be "+
					"completed by Terraform.\n\nCheck that payment_method_id names a saved card on "+
					"this organization — the phasetwo_payment_method data source will find one.")
			return
		}
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not create the cluster", err))
		return
	}

	// Record the id before waiting. Provisioning can take a while, and if the wait fails or the
	// operator interrupts it, the cluster must not be lost from state.
	partial := plan
	partial.ID = types.StringValue(cluster.Id)
	partial.markComputedUnset()
	resp.State.Set(ctx, &partial)

	plan.ID = types.StringValue(cluster.Id)

	cluster, err = r.client.WaitForClusterActive(ctx, cluster.Id, timeout)
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("Cluster did not become active", err))
		return
	}

	r.apply(&plan, cluster)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *clusterResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state clusterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cluster, err := r.client.GetCluster(ctx, state.ID.ValueString())
	if err != nil {
		if api.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not read the cluster", err))
		return
	}

	// A cluster scheduled for deletion still answers reads, but it is on its way out and its
	// name is spent. Treat it as gone so a plan proposes recreating it rather than reporting
	// everything as fine.
	if api.ClusterGone(cluster.Status) {
		resp.Diagnostics.AddWarning(
			"Cluster is no longer usable",
			fmt.Sprintf("Cluster %s is in %s and has been removed from state. Note that its name "+
				"(%q) stays reserved until teardown completes, so recreating it under the same "+
				"name will fail.", cluster.Id, cluster.Status, cluster.Name))
		resp.State.RemoveResource(ctx)
		return
	}

	r.apply(&state, cluster)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update exists only to satisfy the interface: every attribute is RequiresReplace, so the
// framework never calls this with a real change.
func (r *clusterResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan clusterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *clusterResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state clusterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	configured, diags := state.Timeouts.Delete(ctx, defaultClusterDeleteTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout := defaultTimeout(configured, defaultClusterDeleteTimeout)
	ctx, cancel := ctxWithTimeout(ctx, timeout)
	defer cancel()

	id := state.ID.ValueString()
	if err := r.client.DeleteCluster(ctx, id); err != nil {
		if api.IsNotFound(err) {
			return
		}
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not delete the cluster", err))
		return
	}

	last, err := r.client.WaitForClusterGone(ctx, id, timeout)
	if err != nil {
		resp.Diagnostics.AddWarning(
			"Could not confirm the cluster was removed",
			fmt.Sprintf("The delete was accepted, but polling for its result failed: %s\n\n"+
				"The cluster has been removed from state. Check its status in the Phase Two "+
				"console.", err))
		return
	}

	if last == api.ClusterPendingDeletion {
		resp.Diagnostics.AddWarning(
			"Cluster is scheduled for deletion, not deleted",
			fmt.Sprintf("Cluster %s (%q) has moved to PENDING_DELETION. Phase Two tears it down "+
				"at the end of the current billing cycle, and it continues to bill until then.\n\n"+
				"It has been removed from Terraform state. Its name stays reserved until teardown "+
				"completes, so a new cluster cannot reuse it yet.", id, state.Name.ValueString()))
	}
}

func (r *clusterResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// markComputedUnset nulls every computed attribute.
//
// It is used before persisting a partial result mid-Create. Straight after a create these
// attributes are still unknown, and Terraform state cannot hold unknown values — writing them
// would produce a state Terraform core rejects, losing the very id the early save exists to
// keep. Null is the correct stand-in: not yet read.
func (m *clusterModel) markComputedUnset() {
	m.Host = types.StringNull()
	m.Status = types.StringNull()
	m.Owner = types.StringNull()
	m.Variant = types.StringNull()
	m.ResourceLimits = types.StringNull()
	m.CreatedAt = types.StringNull()
}

// apply copies an API cluster onto the model. payment_method_id and billing_period are not
// readable back from the API, so whatever is already in state or plan is left alone.
func (r *clusterResource) apply(m *clusterModel, c *client.Cluster) {
	m.ID = types.StringValue(c.Id)
	m.Name = types.StringValue(c.Name)
	m.Region = types.StringValue(c.Region.Name)
	m.Host = types.StringValue(c.Host)
	m.Status = types.StringValue(string(c.Status))
	m.Tier = types.StringValue(string(c.Tier))
	m.ResourceLimits = types.StringValue(string(c.ResourceLimits))
	m.CreatedAt = epochMillisToRFC3339(c.CreatedAt)
	m.Owner = stringOrNull(c.Owner)
	m.Variant = stringOrNull(c.Variant)
	m.Domain = stringOrNull(c.Domain)

	// The API reports the owning organization as `owner`; organization_id is the request field.
	// Keep them in step on read so an out-of-band move is visible.
	if c.Owner != nil {
		m.OrganizationID = types.StringValue(*c.Owner)
	}
}
