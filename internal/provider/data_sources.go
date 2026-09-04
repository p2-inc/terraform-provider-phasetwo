package provider

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/p2-inc/terraform-provider-phasetwo/internal/api"
)

// ---------------------------------------------------------------------------
// phasetwo_organization
// ---------------------------------------------------------------------------

var (
	_ datasource.DataSource              = (*organizationDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*organizationDataSource)(nil)
)

// NewOrganizationDataSource returns the phasetwo_organization data source.
func NewOrganizationDataSource() datasource.DataSource { return &organizationDataSource{} }

type organizationDataSource struct{ client *api.Client }

type organizationModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	DisplayName types.String `tfsdk:"display_name"`
	Roles       types.List   `tfsdk:"roles"`
}

func (d *organizationDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization"
}

func (d *organizationDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An organization (team) the API client belongs to.\n\n" +
			"Organizations are not managed by this provider — look one up here and pass its `id` " +
			"to `phasetwo_cluster`.\n\n" +
			"Give exactly one of `id` or `name`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "ID of the organization. Set this or `name`.",
			},
			"name": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Name of the organization. Set this or `id`.",
			},
			"display_name": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Human-readable organization name.",
			},
			"roles": schema.ListAttribute{
				Computed:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Roles the API client holds in this organization.",
			},
		},
	}
}

func (d *organizationDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *organizationDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg organizationModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	wantID := cfg.ID.ValueString()
	wantName := cfg.Name.ValueString()
	if (wantID == "") == (wantName == "") {
		resp.Diagnostics.AddError("Specify exactly one of id or name",
			"Look up an organization either by id or by name, not both and not neither.")
		return
	}

	orgs, err := d.client.ListOrgs(ctx)
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not list organizations", err))
		return
	}

	var found *struct {
		ID, Name, Display string
		Roles             []string
	}
	names := make([]string, 0, len(orgs))
	for _, o := range orgs {
		names = append(names, o.Name)
		if (wantID != "" && o.Id == wantID) || (wantName != "" && o.Name == wantName) {
			disp := ""
			if o.DisplayName != nil {
				disp = *o.DisplayName
			}
			found = &struct {
				ID, Name, Display string
				Roles             []string
			}{o.Id, o.Name, disp, o.Roles}
			break
		}
	}

	if found == nil {
		sort.Strings(names)
		what := "id " + wantID
		if wantName != "" {
			what = "name " + wantName
		}
		resp.Diagnostics.AddError("No such organization",
			fmt.Sprintf("No organization with %s is visible to this API secret.\n\nVisible "+
				"organizations: %s", what, strings.Join(names, ", ")))
		return
	}

	roles, diags := types.ListValueFrom(ctx, types.StringType, found.Roles)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	cfg.ID = types.StringValue(found.ID)
	cfg.Name = types.StringValue(found.Name)
	cfg.DisplayName = types.StringValue(found.Display)
	cfg.Roles = roles
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}

// ---------------------------------------------------------------------------
// phasetwo_organizations
// ---------------------------------------------------------------------------

var (
	_ datasource.DataSource              = (*organizationsDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*organizationsDataSource)(nil)
)

// NewOrganizationsDataSource returns the phasetwo_organizations data source.
func NewOrganizationsDataSource() datasource.DataSource { return &organizationsDataSource{} }

type organizationsDataSource struct{ client *api.Client }

type organizationsModel struct {
	Organizations []organizationEntryModel `tfsdk:"organizations"`
}

type organizationEntryModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	DisplayName types.String `tfsdk:"display_name"`
}

func (d *organizationsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organizations"
}

func (d *organizationsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Every organization the API client belongs to.",
		Attributes: map[string]schema.Attribute{
			"organizations": schema.ListNestedAttribute{
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id":           schema.StringAttribute{Computed: true, MarkdownDescription: "ID of the organization."},
						"name":         schema.StringAttribute{Computed: true, MarkdownDescription: "Name of the organization."},
						"display_name": schema.StringAttribute{Computed: true, MarkdownDescription: "Human-readable name."},
					},
				},
				MarkdownDescription: "The organizations, in the order the API returned them.",
			},
		},
	}
}

func (d *organizationsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *organizationsDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	orgs, err := d.client.ListOrgs(ctx)
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not list organizations", err))
		return
	}

	out := organizationsModel{Organizations: make([]organizationEntryModel, 0, len(orgs))}
	for _, o := range orgs {
		out.Organizations = append(out.Organizations, organizationEntryModel{
			ID:          types.StringValue(o.Id),
			Name:        types.StringValue(o.Name),
			DisplayName: stringOrNull(o.DisplayName),
		})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &out)...)
}

// ---------------------------------------------------------------------------
// phasetwo_payment_method
// ---------------------------------------------------------------------------

var (
	_ datasource.DataSource              = (*paymentMethodDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*paymentMethodDataSource)(nil)
)

// NewPaymentMethodDataSource returns the phasetwo_payment_method data source.
func NewPaymentMethodDataSource() datasource.DataSource { return &paymentMethodDataSource{} }

type paymentMethodDataSource struct{ client *api.Client }

type paymentMethodModel struct {
	OrganizationID types.String `tfsdk:"organization_id"`
	ID             types.String `tfsdk:"id"`
	Default        types.Bool   `tfsdk:"default"`

	Type     types.String `tfsdk:"type"`
	Brand    types.String `tfsdk:"brand"`
	Last4    types.String `tfsdk:"last4"`
	ExpMonth types.Int64  `tfsdk:"exp_month"`
	ExpYear  types.Int64  `tfsdk:"exp_year"`
	InUse    types.Bool   `tfsdk:"in_use_by_active_subscription"`
}

func (d *paymentMethodDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_payment_method"
}

func (d *paymentMethodDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A saved payment method on an organization.\n\n" +
			"Payment methods are not managed by this provider — add a card in the Phase Two " +
			"console, then look it up here to pass to `phasetwo_cluster`.\n\n" +
			"Give either `id`, or `default = true` to take the organization's default card.",
		Attributes: map[string]schema.Attribute{
			"organization_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "ID of the organization the payment method belongs to.",
			},
			"id": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "ID of the payment method. Set this or `default`.",
			},
			"default": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Set to `true` to select the organization's default payment " +
					"method. On read, reports whether the selected method is the default.",
			},
			"type":  schema.StringAttribute{Computed: true, MarkdownDescription: "Payment method type, e.g. `card`."},
			"brand": schema.StringAttribute{Computed: true, MarkdownDescription: "Card brand."},
			"last4": schema.StringAttribute{Computed: true, MarkdownDescription: "Last four digits of the card."},
			"exp_month": schema.Int64Attribute{
				Computed: true, MarkdownDescription: "Card expiry month.",
			},
			"exp_year": schema.Int64Attribute{
				Computed: true, MarkdownDescription: "Card expiry year.",
			},
			"in_use_by_active_subscription": schema.BoolAttribute{
				Computed: true,
				MarkdownDescription: "Whether an active subscription is using this payment method. " +
					"One that is in use cannot be removed.",
			},
		},
	}
}

func (d *paymentMethodDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *paymentMethodDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg paymentMethodModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	wantID := cfg.ID.ValueString()
	wantDefault := !cfg.Default.IsNull() && cfg.Default.ValueBool()
	if wantID == "" && !wantDefault {
		resp.Diagnostics.AddError("Specify id or default",
			"Set id to select a specific payment method, or default = true to use the "+
				"organization's default one.")
		return
	}

	orgID := cfg.OrganizationID.ValueString()
	methods, err := d.client.ListPaymentMethods(ctx, orgID)
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not list payment methods", err))
		return
	}
	if len(methods) == 0 {
		resp.Diagnostics.AddError("No payment methods",
			fmt.Sprintf("Organization %s has no saved payment methods. Add a card in the Phase "+
				"Two console before creating a cluster — the API's own add-a-card flow is a "+
				"browser redirect that Terraform cannot complete.", orgID))
		return
	}

	for _, m := range methods {
		if (wantID != "" && m.Id == wantID) || (wantID == "" && wantDefault && m.IsDefault) {
			cfg.ID = types.StringValue(m.Id)
			cfg.Default = types.BoolValue(m.IsDefault)
			cfg.Type = types.StringValue(m.Type)
			cfg.Brand = stringOrNull(m.Brand)
			cfg.Last4 = stringOrNull(m.Last4)
			cfg.InUse = types.BoolValue(m.InUseByActiveSubscription)
			cfg.ExpMonth = int64OrNull(m.ExpMonth)
			cfg.ExpYear = int64OrNull(m.ExpYear)
			resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
			return
		}
	}

	if wantDefault {
		resp.Diagnostics.AddError("No default payment method",
			fmt.Sprintf("Organization %s has %d saved payment method(s), but none is marked as "+
				"the default. Set one as default in the Phase Two console, or select one by id.",
				orgID, len(methods)))
		return
	}
	resp.Diagnostics.AddError("No such payment method",
		fmt.Sprintf("Organization %s has no payment method with id %s.", orgID, wantID))
}

func int64OrNull(v *int64) types.Int64 {
	if v == nil {
		return types.Int64Null()
	}
	return types.Int64Value(*v)
}

// ---------------------------------------------------------------------------
// phasetwo_regions
// ---------------------------------------------------------------------------

var (
	_ datasource.DataSource              = (*regionsDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*regionsDataSource)(nil)
)

// NewRegionsDataSource returns the phasetwo_regions data source.
func NewRegionsDataSource() datasource.DataSource { return &regionsDataSource{} }

type regionsDataSource struct{ client *api.Client }

type regionsModel struct {
	Names types.List `tfsdk:"names"`
}

func (d *regionsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_regions"
}

func (d *regionsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Regions a new dedicated cluster can be provisioned into.",
		Attributes: map[string]schema.Attribute{
			"names": schema.ListAttribute{
				Computed:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Region names, e.g. `US_EAST_1`.",
			},
		},
	}
}

func (d *regionsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *regionsDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	regions, err := d.client.ListRegions(ctx)
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not list regions", err))
		return
	}

	names := make([]string, 0, len(regions))
	for _, r := range regions {
		names = append(names, string(r))
	}
	list, diags := types.ListValueFrom(ctx, types.StringType, names)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &regionsModel{Names: list})...)
}
