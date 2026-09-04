package provider

import (
	"context"
	"fmt"
	"strings"

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

var (
	_ resource.Resource                   = (*environmentVariableResource)(nil)
	_ resource.ResourceWithConfigure      = (*environmentVariableResource)(nil)
	_ resource.ResourceWithImportState    = (*environmentVariableResource)(nil)
	_ resource.ResourceWithValidateConfig = (*environmentVariableResource)(nil)
)

// NewEnvironmentVariableResource returns the phasetwo_cluster_environment_variable resource.
func NewEnvironmentVariableResource() resource.Resource { return &environmentVariableResource{} }

type environmentVariableResource struct {
	client *api.Client
}

type environmentVariableModel struct {
	ID        types.String `tfsdk:"id"`
	ClusterID types.String `tfsdk:"cluster_id"`
	Name      types.String `tfsdk:"name"`
	Value     types.String `tfsdk:"value"`
	Type      types.String `tfsdk:"type"`
}

func (r *environmentVariableResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cluster_environment_variable"
}

func (r *environmentVariableResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A custom environment variable on a dedicated cluster's Keycloak " +
			"deployment.\n\n" +
			"~> **Every change here restarts the cluster's Keycloak deployment.** The provider " +
			"serializes these changes per cluster and waits for each restart to finish, so " +
			"declaring several variables on one cluster works — but an apply that touches many of " +
			"them will take as long as that many restarts.\n\n" +
			"Only custom variables are allowed: a name must either start with `KC_SPI_` or not " +
			"start with `KC_` at all.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Unique identifier of the environment variable.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"cluster_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "ID of the cluster.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Variable name. Must start with `KC_SPI_`, or not start with " +
					"`KC_`.",
			},
			"value": schema.StringAttribute{
				Required:  true,
				Sensitive: true,
				MarkdownDescription: "Variable value.\n\n" +
					"When `type` is `SECRET` the API never returns this again — reads come back " +
					"masked — so Terraform cannot detect a change made outside Terraform. The value " +
					"in state is whatever was last applied.",
			},
			"type": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString("STRING"),
				MarkdownDescription: "How the value is stored: `STRING` (the default) or `SECRET`, " +
					"which keeps it in AWS Parameter Store as an encrypted parameter and masks it " +
					"on read.",
				Validators: []validator.String{
					stringvalidator.OneOf(string(api.EnvString), string(api.EnvSecret)),
				},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
		},
	}
}

func (r *environmentVariableResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

// ValidateConfig applies the server's own name rule at plan time, so a bad name is a plan error
// rather than a 400 partway through an apply.
func (r *environmentVariableResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg environmentVariableModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || cfg.Name.IsUnknown() || cfg.Name.IsNull() {
		return
	}
	if !api.ValidEnvVarName(cfg.Name.ValueString()) {
		resp.Diagnostics.AddAttributeError(path.Root("name"),
			"Invalid environment variable name",
			fmt.Sprintf("%q is a built-in Keycloak variable. Only custom variables can be set: "+
				"the name must either start with KC_SPI_ or not start with KC_.",
				cfg.Name.ValueString()))
	}
}

func (r *environmentVariableResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan environmentVariableModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	t := client.EnvironmentVariableType(plan.Type.ValueString())
	created, err := r.client.CreateEnvVar(ctx, plan.ClusterID.ValueString(),
		client.EnvironmentVariableRequest{
			Name:  plan.Name.ValueString(),
			Value: plan.Value.ValueString(),
			Type:  &t,
		})
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not create the environment variable", err))
		return
	}

	plan.ID = types.StringValue(created.Id)
	plan.Name = types.StringValue(created.Name)
	plan.Type = types.StringValue(string(created.Type))
	// Value deliberately not taken from the response: for SECRET it would be the mask.
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *environmentVariableResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state environmentVariableModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	ev, err := r.client.GetEnvVar(ctx, state.ClusterID.ValueString(), state.ID.ValueString())
	if err != nil {
		if api.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not read the environment variable", err))
		return
	}

	state.Name = types.StringValue(ev.Name)
	state.Type = types.StringValue(string(ev.Type))

	// A SECRET reads back as a fixed mask, and a STRING whose value happens to equal the mask is
	// indistinguishable from one. Either way, overwriting state with the mask would show drift on
	// every plan and then "correct" the real value to literal asterisks. Keep what we last applied.
	if ev.Value != api.SecretValueMask {
		state.Value = types.StringValue(ev.Value)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *environmentVariableResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state environmentVariableModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	plan.ID = state.ID
	t := client.EnvironmentVariableType(plan.Type.ValueString())
	updated, err := r.client.UpdateEnvVar(ctx, plan.ClusterID.ValueString(), plan.ID.ValueString(),
		client.EnvironmentVariableRequest{
			Name:  plan.Name.ValueString(),
			Value: plan.Value.ValueString(),
			Type:  &t,
		})
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not update the environment variable", err))
		return
	}

	plan.Name = types.StringValue(updated.Name)
	plan.Type = types.StringValue(string(updated.Type))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *environmentVariableResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state environmentVariableModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.DeleteEnvVar(ctx, state.ClusterID.ValueString(), state.ID.ValueString())
	if err != nil && !api.IsNotFound(err) {
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not delete the environment variable", err))
	}
}

// ImportState takes `cluster_id/env_var_id`.
//
// A SECRET's value cannot be read back, so an imported SECRET has no value in state and the
// first plan will show a change. That is unavoidable and is noted in the docs.
func (r *environmentVariableResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("Expected %q, got %q", "<cluster_id>/<env_var_id>", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cluster_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
}
