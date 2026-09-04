package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/p2-inc/terraform-provider-phasetwo/internal/api"
	"github.com/p2-inc/terraform-provider-phasetwo/internal/client"
)

var (
	_ resource.Resource                = (*clusterIPRulesResource)(nil)
	_ resource.ResourceWithConfigure   = (*clusterIPRulesResource)(nil)
	_ resource.ResourceWithImportState = (*clusterIPRulesResource)(nil)
)

// NewClusterIPRulesResource returns the phasetwo_cluster_ip_rules resource.
func NewClusterIPRulesResource() resource.Resource { return &clusterIPRulesResource{} }

type clusterIPRulesResource struct {
	client *api.Client
}

type ipRuleModel struct {
	Alias   types.String `tfsdk:"alias"`
	Address types.String `tfsdk:"address"`
}

type clusterIPRulesModel struct {
	ID         types.String  `tfsdk:"id"`
	ClusterID  types.String  `tfsdk:"cluster_id"`
	AdminAllow []ipRuleModel `tfsdk:"admin_allow"`
	RealmAllow []ipRuleModel `tfsdk:"realm_allow"`
	RealmBlock []ipRuleModel `tfsdk:"realm_block"`
}

func (r *clusterIPRulesResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cluster_ip_rules"
}

func ipRuleBlockSchema(desc string) schema.ListNestedBlock {
	return schema.ListNestedBlock{
		MarkdownDescription: desc,
		NestedObject: schema.NestedBlockObject{
			Attributes: map[string]schema.Attribute{
				"alias": schema.StringAttribute{
					Required:            true,
					MarkdownDescription: "Human-readable label for the rule.",
				},
				"address": schema.StringAttribute{
					Required:            true,
					MarkdownDescription: "IP address or CIDR block, e.g. `203.0.113.4` or `203.0.113.0/24`.",
				},
			},
		},
	}
}

func (r *clusterIPRulesResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "IP allow and deny rules for a dedicated cluster.\n\n" +
			"This is a singleton holding all three rule lists, because the API replaces a whole " +
			"category at a time and has no per-rule delete. Declare at most one of these per " +
			"cluster — a second would silently fight the first.\n\n" +
			"Every category is sent on each apply, so removing a block from configuration removes " +
			"those rules. An empty category means no rules in it.\n\n" +
			"~> An empty `admin_allow` means the admin console is reachable from anywhere. Adding " +
			"the first entry restricts it to exactly that list — make sure it covers you before " +
			"applying, or you will lock yourself out of the console.\n\n" +
			"The number of rules a cluster may hold depends on its tier: none on `starter`, 2 on " +
			"`premium`, unlimited on `enterprise`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Same as `cluster_id` — the rules are a property of the " +
					"cluster rather than a resource with its own identity.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"cluster_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "ID of the cluster.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
		},
		Blocks: map[string]schema.Block{
			"admin_allow": ipRuleBlockSchema(
				"Addresses allowed to reach the cluster's admin console. When empty, the console " +
					"is not IP-restricted."),
			"realm_allow": ipRuleBlockSchema(
				"Addresses allowed to reach the cluster's realm endpoints. When empty, the realm " +
					"endpoints are not IP-restricted."),
			"realm_block": ipRuleBlockSchema(
				"Addresses blocked from reaching the cluster's realm endpoints."),
		},
	}
}

func (r *clusterIPRulesResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

func (r *clusterIPRulesResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan clusterIPRulesModel
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

func (r *clusterIPRulesResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state clusterIPRulesModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	rules, err := r.client.GetIPRules(ctx, state.ClusterID.ValueString())
	if err != nil {
		if api.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not read the cluster's IP rules", err))
		return
	}

	state.ID = state.ClusterID
	state.AdminAllow = fromAPIRules(rules.AdminAllowedIpRules)
	state.RealmAllow = fromAPIRules(rules.RealmAllowedIpRules)
	state.RealmBlock = fromAPIRules(rules.RealmBlockedIpRules)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *clusterIPRulesResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan clusterIPRulesModel
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

// Delete clears all three categories. There is no per-rule delete, so replacing every category
// with an empty list is what "remove these rules" means.
func (r *clusterIPRulesResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state clusterIPRulesModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	empty := []client.IpRule{}
	_, err := r.client.SetIPRules(ctx, state.ClusterID.ValueString(), client.IpRestrictionsRequest{
		AdminAllowedIpRules: &empty,
		RealmAllowedIpRules: &empty,
		RealmBlockedIpRules: &empty,
	})
	if err != nil {
		if api.IsNotFound(err) {
			return
		}
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not clear the cluster's IP rules", err))
	}
}

func (r *clusterIPRulesResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cluster_id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// set sends all three categories and copies the server's answer back onto the model.
func (r *clusterIPRulesResource) set(ctx context.Context, plan *clusterIPRulesModel) diag.Diagnostics {
	var diags diag.Diagnostics

	admin := toAPIRules(plan.AdminAllow)
	realmAllow := toAPIRules(plan.RealmAllow)
	realmBlock := toAPIRules(plan.RealmBlock)

	rules, err := r.client.SetIPRules(ctx, plan.ClusterID.ValueString(), client.IpRestrictionsRequest{
		AdminAllowedIpRules: &admin,
		RealmAllowedIpRules: &realmAllow,
		RealmBlockedIpRules: &realmBlock,
	})
	if err != nil {
		diags.Append(apiErrorDiagnostic("Could not set the cluster's IP rules", err))
		return diags
	}

	plan.ID = plan.ClusterID
	plan.AdminAllow = fromAPIRules(rules.AdminAllowedIpRules)
	plan.RealmAllow = fromAPIRules(rules.RealmAllowedIpRules)
	plan.RealmBlock = fromAPIRules(rules.RealmBlockedIpRules)
	return diags
}

func toAPIRules(in []ipRuleModel) []client.IpRule {
	out := make([]client.IpRule, 0, len(in))
	for _, r := range in {
		out = append(out, client.IpRule{
			Alias:   r.Alias.ValueString(),
			Address: r.Address.ValueString(),
		})
	}
	return out
}

// fromAPIRules keeps only alias and address. The API also returns id, endpointType, allowDeny
// and evaluationOrder, but those are all implied by which category the rule came back in, so
// surfacing them would be redundant state that can only drift.
func fromAPIRules(in []client.IpRuleRepresentation) []ipRuleModel {
	out := make([]ipRuleModel, 0, len(in))
	for _, r := range in {
		out = append(out, ipRuleModel{
			Alias:   types.StringValue(r.Alias),
			Address: types.StringValue(r.Address),
		})
	}
	return out
}
