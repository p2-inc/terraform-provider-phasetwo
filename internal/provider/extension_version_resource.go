package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int32planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/p2-inc/terraform-provider-phasetwo/internal/api"
	"github.com/p2-inc/terraform-provider-phasetwo/internal/client"
)

var (
	_ resource.Resource                = (*extensionVersionResource)(nil)
	_ resource.ResourceWithConfigure   = (*extensionVersionResource)(nil)
	_ resource.ResourceWithImportState = (*extensionVersionResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*extensionVersionResource)(nil)
)

// NewExtensionVersionResource returns the phasetwo_cluster_extension_version resource.
func NewExtensionVersionResource() resource.Resource { return &extensionVersionResource{} }

type extensionVersionResource struct {
	client *api.Client
}

type extensionVersionModel struct {
	ID                   types.String `tfsdk:"id"`
	ClusterID            types.String `tfsdk:"cluster_id"`
	ExtensionID          types.String `tfsdk:"extension_id"`
	Source               types.String `tfsdk:"source"`
	SourceHash           types.String `tfsdk:"source_hash"`
	Label                types.String `tfsdk:"label"`
	KeycloakMajorVersion types.Int32  `tfsdk:"keycloak_major_version"`

	Valid       types.Bool   `tfsdk:"valid"`
	ScanState   types.String `tfsdk:"scan_state"`
	RiskScore   types.Int32  `tfsdk:"risk_score"`
	ResourceKey types.String `tfsdk:"resource_key"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

func (r *extensionVersionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cluster_extension_version"
}

func (r *extensionVersionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An uploaded artifact for a `phasetwo_cluster_extension`.\n\n" +
			"Applying this uploads the file and reconciles the cluster, which **restarts its " +
			"Keycloak deployment**. The provider serializes that against other restart-causing " +
			"changes on the same cluster.\n\n" +
			"Set `keycloak_major_version` for `EXTENSION` and `THEME` extensions; leave it unset " +
			"for `PASSWORD_BLACKLIST`, which is version-independent.\n\n" +
			"Changing the file on disk is detected through `source_hash`, so a rebuild of the " +
			"same path produces a new version.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Unique identifier of the extension version.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"cluster_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "ID of the cluster.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"extension_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "ID of the extension this version belongs to.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"source": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Path to the file to upload — a jar for `EXTENSION` and " +
					"`THEME`, the blacklist file for `PASSWORD_BLACKLIST`. Read from the machine " +
					"running Terraform.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"source_hash": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "SHA-256 of the uploaded file. Computed from `source` when " +
					"not set; setting it explicitly (for example from `filesha256`) is useful when " +
					"the file is produced by another resource.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"label": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Label for this version. Defaults to the file's base name.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"keycloak_major_version": schema.Int32Attribute{
				Optional: true,
				MarkdownDescription: "Keycloak major version this artifact targets. Required for " +
					"`EXTENSION` and `THEME`; must be omitted for `PASSWORD_BLACKLIST`. Allowed " +
					"values come from the `phasetwo_cluster` the extension belongs to.",
				PlanModifiers: []planmodifier.Int32{int32planmodifier.RequiresReplace()},
			},

			"valid": schema.BoolAttribute{
				Computed: true,
				MarkdownDescription: "Whether the version is considered valid for deployment. A " +
					"security scan can mark it invalid, in which case it must be approved in the " +
					"Phase Two console.",
			},
			"scan_state": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "State of the security scan of this artifact.",
			},
			"risk_score": schema.Int32Attribute{
				Computed:            true,
				MarkdownDescription: "Risk score from the security scan, when one has run.",
			},
			"resource_key": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "S3 key the artifact was stored under.",
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "When the version was created, as an RFC 3339 timestamp.",
			},
		},
	}
}

func (r *extensionVersionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

// ModifyPlan fills in source_hash and label from the file on disk, so a changed file shows up as
// a planned replacement rather than being noticed only at apply time.
func (r *extensionVersionResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return // destroy
	}

	var plan extensionVersionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || plan.Source.IsUnknown() || plan.Source.IsNull() {
		return
	}
	src := plan.Source.ValueString()

	if plan.Label.IsUnknown() || plan.Label.IsNull() {
		plan.Label = types.StringValue(baseName(src))
	}

	if plan.SourceHash.IsUnknown() || plan.SourceHash.IsNull() {
		sum, err := fileSHA256(src)
		if err != nil {
			// Not fatal at plan time: the file may be produced later in the same apply. Leave the
			// hash unknown and let Create surface a real read failure.
			resp.Diagnostics.AddAttributeWarning(path.Root("source"),
				"Could not hash the source file during planning",
				fmt.Sprintf("%s\n\nIf this file is generated by another resource in the same "+
					"configuration, set source_hash explicitly (for example with filesha256) so "+
					"changes are detected reliably.", err))
			plan.SourceHash = types.StringUnknown()
		} else {
			plan.SourceHash = types.StringValue(sum)
		}
	}

	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

func (r *extensionVersionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan extensionVersionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	clusterID := plan.ClusterID.ValueString()
	extensionID := plan.ExtensionID.ValueString()

	ext, err := r.client.GetExtension(ctx, clusterID, extensionID)
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not read the extension", err))
		return
	}
	versioned := api.VersionDependent(ext.ResourceType)

	// The two flows are different API calls, and picking the wrong one fails with a 404 that does
	// not explain itself. Check the combination here instead.
	switch {
	case versioned && plan.KeycloakMajorVersion.IsNull():
		resp.Diagnostics.AddAttributeError(path.Root("keycloak_major_version"),
			"keycloak_major_version is required for this extension",
			fmt.Sprintf("Extension %q is a %s, which is tied to a Keycloak major version.",
				ext.Name, ext.ResourceType))
		return
	case !versioned && !plan.KeycloakMajorVersion.IsNull():
		resp.Diagnostics.AddAttributeError(path.Root("keycloak_major_version"),
			"keycloak_major_version must not be set for this extension",
			fmt.Sprintf("Extension %q is a %s, which is version-independent.",
				ext.Name, ext.ResourceType))
		return
	}

	payload, err := os.ReadFile(plan.Source.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("source"),
			"Could not read the source file", err.Error())
		return
	}

	// Recompute rather than trusting the planned hash: the file may have been rebuilt between
	// plan and apply, and state should describe what was actually uploaded.
	sum := sha256.Sum256(payload)
	plan.SourceHash = types.StringValue(hex.EncodeToString(sum[:]))

	label := plan.Label.ValueString()
	if label == "" {
		label = baseName(plan.Source.ValueString())
		plan.Label = types.StringValue(label)
	}

	var version *client.ExtensionVersion
	if versioned {
		version, err = r.client.UploadVersion(ctx, clusterID, extensionID,
			plan.KeycloakMajorVersion.ValueInt32(), label, payload)
	} else {
		version, err = r.client.UploadStandalone(ctx, clusterID, extensionID, label, payload)
	}
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not upload the extension version", err))
		return
	}

	r.apply(&plan, version)
	resp.State.Set(ctx, &plan)

	if err := r.client.ReconcileExtensions(ctx, clusterID); err != nil {
		resp.Diagnostics.AddWarning(
			"Version uploaded but the cluster was not reconciled",
			fmt.Sprintf("The artifact is stored, but the running Keycloak deployment will not "+
				"pick it up until the cluster restarts: %s\n\nApply again, or restart the cluster "+
				"from the Phase Two console.", err))
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *extensionVersionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state extensionVersionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// There is no get-one-version operation, so find it on the parent extension.
	ext, err := r.client.GetExtension(ctx, state.ClusterID.ValueString(), state.ExtensionID.ValueString())
	if err != nil {
		if api.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not read the extension", err))
		return
	}

	wanted := state.ID.ValueString()
	if ext.Versions != nil {
		for i := range *ext.Versions {
			v := (*ext.Versions)[i]
			if v.Id == wanted {
				r.apply(&state, &v)
				resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
				return
			}
		}
	}
	resp.State.RemoveResource(ctx)
}

// Update is unreachable: every configurable attribute forces replacement.
func (r *extensionVersionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan extensionVersionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *extensionVersionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state extensionVersionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	clusterID := state.ClusterID.ValueString()
	err := r.client.DeleteExtensionVersion(ctx, clusterID,
		state.ExtensionID.ValueString(), state.ID.ValueString())
	if err != nil && !api.IsNotFound(err) {
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not delete the extension version", err))
		return
	}

	if err := r.client.ReconcileExtensions(ctx, clusterID); err != nil && !api.IsNotFound(err) {
		resp.Diagnostics.AddWarning(
			"Version deleted but the cluster was not reconciled",
			fmt.Sprintf("The version record is gone, but the running deployment keeps its files "+
				"until the cluster restarts: %s", err))
	}
}

// ImportState takes `cluster_id/extension_id/version_id`. `source` cannot be recovered from the
// API, so an imported version will plan a replacement until `source` matches what was uploaded.
func (r *extensionVersionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("Expected %q, got %q", "<cluster_id>/<extension_id>/<version_id>", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cluster_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("extension_id"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[2])...)
}

func (r *extensionVersionResource) apply(m *extensionVersionModel, v *client.ExtensionVersion) {
	m.ID = types.StringValue(v.Id)
	m.Valid = types.BoolValue(v.Valid)
	m.ScanState = types.StringValue(v.ScanState)
	m.ResourceKey = stringOrNull(v.ResourceKey)
	m.CreatedAt = epochMillisPtrToRFC3339(v.CreatedAt)

	if v.Label != nil && *v.Label != "" {
		m.Label = types.StringValue(*v.Label)
	}
	if v.RiskScore != nil {
		m.RiskScore = types.Int32Value(*v.RiskScore)
	} else {
		m.RiskScore = types.Int32Null()
	}
	if v.KeycloakMajorVersion != nil {
		m.KeycloakMajorVersion = types.Int32Value(*v.KeycloakMajorVersion)
	}
}

func fileSHA256(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func baseName(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
