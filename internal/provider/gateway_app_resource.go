package provider

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/p2-inc/terraform-provider-phasetwo/internal/api"
	"github.com/p2-inc/terraform-provider-phasetwo/internal/client"
)

var (
	_ resource.Resource                = (*gatewayAppResource)(nil)
	_ resource.ResourceWithConfigure   = (*gatewayAppResource)(nil)
	_ resource.ResourceWithImportState = (*gatewayAppResource)(nil)
)

// The claims a header or a policy condition can use. email_verified is the only boolean, and the
// API compares claims_equal values by type, so it is sent as a JSON boolean.
var (
	gatewayHeaderClaims = []string{
		"sub", "email", "email_verified", "preferred_username", "name", "given_name",
		"family_name", "groups", "realm_roles", "client_roles",
	}
	gatewayAssertionClaims = []string{
		"email", "email_verified", "preferred_username", "name", "given_name", "family_name",
		"groups", "realm_roles", "client_roles",
	}
	gatewayBooleanClaims = map[string]bool{"email_verified": true}

	// The API lower-cases the host and strips trailing slashes; requiring that form here keeps a
	// refresh from planning a change nobody made.
	gatewayHostnameRE  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)
	gatewayOriginURLRE = regexp.MustCompile(`^https://[a-z0-9.-]+(:(443|8443))?(/[^?#\s]*[^/?#\s])?$`)
	gatewayHeaderRE    = regexp.MustCompile(`^X-Phasetwo-Auth-[A-Za-z0-9-]+$`)
	gatewayMethodRE    = regexp.MustCompile(`^[A-Z]+$`)
)

// NewGatewayAppResource returns the phasetwo_gateway_app resource.
func NewGatewayAppResource() resource.Resource { return &gatewayAppResource{} }

type gatewayAppResource struct {
	client *api.Client
}

type gatewayAppModel struct {
	ID                    types.String `tfsdk:"id"`
	RealmID               types.String `tfsdk:"realm_id"`
	Name                  types.String `tfsdk:"name"`
	PublicHostname        types.String `tfsdk:"public_hostname"`
	OriginURL             types.String `tfsdk:"origin_url"`
	HealthPath            types.String `tfsdk:"health_path"`
	Policy                types.Object `tfsdk:"policy"`
	HeaderMappings        types.Map    `tfsdk:"header_mappings"`
	AssertionEnabled      types.Bool   `tfsdk:"assertion_enabled"`
	AssertionClaims       types.List   `tfsdk:"assertion_claims"`
	IdleTimeoutSeconds    types.Int64  `tfsdk:"idle_timeout_seconds"`
	MaxSessionSeconds     types.Int64  `tfsdk:"max_session_seconds"`
	PostLogoutRedirectURI types.String `tfsdk:"post_logout_redirect_uri"`

	OriginSecret       types.String `tfsdk:"origin_secret"`
	OriginSecretHeader types.String `tfsdk:"origin_secret_header"`
	Status             types.String `tfsdk:"status"`
	ClientID           types.String `tfsdk:"client_id"`
	Issuer             types.String `tfsdk:"issuer"`
	CallbackURL        types.String `tfsdk:"callback_url"`
	AssertionIssuer    types.String `tfsdk:"assertion_issuer"`
	JWKSURL            types.String `tfsdk:"jwks_url"`
	DNSTarget          types.String `tfsdk:"dns_target"`
}

type gatewayPolicyModel struct {
	Default types.String       `tfsdk:"default"`
	Rules   []gatewayRuleModel `tfsdk:"rules"`
}

type gatewayRuleModel struct {
	PathPrefix     types.String `tfsdk:"path_prefix"`
	Methods        types.List   `tfsdk:"methods"`
	Effect         types.String `tfsdk:"effect"`
	RealmRolesAny  types.List   `tfsdk:"realm_roles_any"`
	ClientRolesAny types.List   `tfsdk:"client_roles_any"`
	GroupsAny      types.List   `tfsdk:"groups_any"`
	ClaimsEqual    types.Map    `tfsdk:"claims_equal"`
}

var (
	gatewayRuleAttrTypes = map[string]attr.Type{
		"path_prefix":      types.StringType,
		"methods":          types.ListType{ElemType: types.StringType},
		"effect":           types.StringType,
		"realm_roles_any":  types.ListType{ElemType: types.StringType},
		"client_roles_any": types.ListType{ElemType: types.StringType},
		"groups_any":       types.ListType{ElemType: types.StringType},
		"claims_equal":     types.MapType{ElemType: types.StringType},
	}
	gatewayPolicyAttrTypes = map[string]attr.Type{
		"default": types.StringType,
		"rules":   types.ListType{ElemType: types.ObjectType{AttrTypes: gatewayRuleAttrTypes}},
	}
)

func (r *gatewayAppResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_gateway_app"
}

func nonEmptyNames() []validator.List {
	return []validator.List{
		listvalidator.SizeAtLeast(1),
		listvalidator.ValueStringsAre(stringvalidator.LengthBetween(1, 256)),
	}
}

func (r *gatewayAppResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	defaultPolicy := types.ObjectValueMust(gatewayPolicyAttrTypes, map[string]attr.Value{
		"default": types.StringValue("authenticated"),
		"rules":   types.ListValueMust(types.ObjectType{AttrTypes: gatewayRuleAttrTypes}, nil),
	})

	resp.Schema = schema.Schema{
		MarkdownDescription: "A website behind Keycloak login with [Gateway]" +
			"(https://phasetwo.io/docs/self-service/gateway/): users of the realm sign in " +
			"before they reach it, and the website itself does not change.\n\n" +
			"Creating an app creates its OIDC client in the realm, generates the origin secret " +
			"and requests the public hostname's certificate. It does **not** serve traffic. To go " +
			"live:\n\n" +
			"1. Make your origin refuse requests that lack `origin_secret` in the " +
			"`X-Phasetwo-Origin-Secret` header.\n" +
			"2. Point `public_hostname` at `dns_target` with a CNAME.\n" +
			"3. Add a [`phasetwo_gateway_app_activation`](gateway_app_activation), which verifies " +
			"the origin and starts serving.\n\n" +
			"~> **`origin_secret` is stored in Terraform state.** The API returns it once, on " +
			"create, and Terraform is the only place left to keep it. Treat the state as secret. " +
			"An imported app has no `origin_secret`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "App ID. Also the `aud` of the identity assertions the " +
					"Gateway sends to the origin.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"realm_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "ID of the realm (deployment) whose users sign in.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Display name.",
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 255)},
			},
			"public_hostname": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Hostname users visit, e.g. `admin.example.com`, in lower " +
					"case. Changing it replaces the app, which creates a new origin secret.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{stringvalidator.RegexMatches(gatewayHostnameRE,
					"must be a lower-case DNS name")},
			},
			"origin_url": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Where the website runs, e.g. " +
					"`https://admin-origin.example.com`: HTTPS with a valid certificate, port 443 " +
					"or 8443, a different hostname from `public_hostname`, an optional base path " +
					"and no trailing slash. Cannot change while the app is active; remove its " +
					"`phasetwo_gateway_app_activation` first.",
				Validators: []validator.String{stringvalidator.RegexMatches(gatewayOriginURLRE,
					"must be https://<lower-case host>[:443|:8443][/path] with no trailing slash, "+
						"query or fragment")},
			},
			"health_path": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString("/"),
				MarkdownDescription: "A path on the origin that returns 2xx when the secret is " +
					"present and has no side effects. Verification sends four requests to it. " +
					"Defaults to `/`.",
				Validators: []validator.String{stringvalidator.RegexMatches(
					regexp.MustCompile(`^/([^/].*)?$`), "must be a path starting with a single /")},
			},
			"policy": schema.SingleNestedAttribute{
				Optional: true,
				Computed: true,
				Default:  objectdefault.StaticValue(defaultPolicy),
				MarkdownDescription: "Who may reach which paths. Without it, any user who can " +
					"sign in to the realm reaches every path.",
				Attributes: map[string]schema.Attribute{
					"default": schema.StringAttribute{
						Optional: true,
						Computed: true,
						Default:  stringdefault.StaticString("authenticated"),
						MarkdownDescription: "`authenticated` (any signed-in user, the default) or " +
							"`deny` (only paths a rule matches).",
						Validators: []validator.String{
							stringvalidator.OneOf("authenticated", "deny"),
						},
					},
					"rules": schema.ListNestedAttribute{
						Optional: true,
						Computed: true,
						Default: listdefault.StaticValue(types.ListValueMust(
							types.ObjectType{AttrTypes: gatewayRuleAttrTypes}, nil)),
						MarkdownDescription: "Every rule whose `path_prefix` and `methods` match " +
							"a request applies, and all of them must pass; `deny` wins. Within a " +
							"rule each list is \"any of\", and the lists combine with AND.",
						Validators: []validator.List{listvalidator.SizeAtMost(100)},
						NestedObject: schema.NestedAttributeObject{
							Attributes: map[string]schema.Attribute{
								"path_prefix": schema.StringAttribute{
									Required: true,
									MarkdownDescription: "Path prefix, e.g. `/admin`. Matches " +
										"whole path segments, case-insensitively: `/admin` " +
										"matches `/Admin/users` but not `/administrator`.",
									Validators: []validator.String{stringvalidator.RegexMatches(
										regexp.MustCompile(`^/[^%\\;?#]*$`),
										"must start with / and contain no %, \\, ;, ? or #")},
								},
								"methods": schema.ListAttribute{
									Optional:    true,
									ElementType: types.StringType,
									MarkdownDescription: "Upper-case HTTP methods the rule " +
										"applies to; every method when omitted. `GET` covers " +
										"`HEAD`.",
									Validators: []validator.List{
										listvalidator.SizeAtLeast(1),
										listvalidator.ValueStringsAre(stringvalidator.RegexMatches(
											gatewayMethodRE, "must be an upper-case HTTP method")),
									},
								},
								"effect": schema.StringAttribute{
									Optional: true,
									MarkdownDescription: "`require` (the default): the " +
										"conditions must hold. `deny`: always refuse.",
									Validators: []validator.String{
										stringvalidator.OneOf("require", "deny"),
									},
								},
								"realm_roles_any": schema.ListAttribute{
									Optional:            true,
									ElementType:         types.StringType,
									MarkdownDescription: "The user holds at least one of these realm roles.",
									Validators:          nonEmptyNames(),
								},
								"client_roles_any": schema.ListAttribute{
									Optional:    true,
									ElementType: types.StringType,
									MarkdownDescription: "The user holds at least one of these " +
										"roles on the app's client (`client_id`).",
									Validators: nonEmptyNames(),
								},
								"groups_any": schema.ListAttribute{
									Optional:    true,
									ElementType: types.StringType,
									MarkdownDescription: "The user is in at least one of these " +
										"groups, by full path, e.g. `/staff/support`.",
									Validators: nonEmptyNames(),
								},
								"claims_equal": schema.MapAttribute{
									Optional:    true,
									ElementType: types.StringType,
									MarkdownDescription: "Claims that must equal these values, " +
										"e.g. `{ email_verified = \"true\" }`. Keys are the " +
										"claims `header_mappings` allows.",
									Validators: []validator.Map{
										mapvalidator.KeysAre(stringvalidator.OneOf(gatewayHeaderClaims...)),
									},
								},
							},
						},
					},
				},
			},
			"header_mappings": schema.MapAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				Default:     mapdefault.StaticValue(types.MapValueMust(types.StringType, nil)),
				MarkdownDescription: "Headers to send to the origin on every request, from header " +
					"name (`X-Phasetwo-Auth-*`) to claim: `sub`, `email`, `email_verified`, " +
					"`preferred_username`, `name`, `given_name`, `family_name`, `groups`, " +
					"`realm_roles` or `client_roles`. The Gateway removes any header with these " +
					"names that a browser sends. Trust them only at an origin that requires the " +
					"origin secret.",
				Validators: []validator.Map{
					mapvalidator.SizeAtMost(20),
					mapvalidator.KeysAre(stringvalidator.RegexMatches(gatewayHeaderRE,
						"must be an X-Phasetwo-Auth-* header name")),
					mapvalidator.ValueStringsAre(stringvalidator.OneOf(gatewayHeaderClaims...)),
				},
			},
			"assertion_enabled": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				MarkdownDescription: "Send a signed JWT in `X-Phasetwo-Auth-Assertion` on every " +
					"request, valid for 60 seconds, so the origin can verify who the user is " +
					"against `jwks_url`.",
			},
			"assertion_claims": schema.ListAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				Default: listdefault.StaticValue(types.ListValueMust(types.StringType, []attr.Value{
					types.StringValue("email"), types.StringValue("preferred_username"),
				})),
				MarkdownDescription: "Claims in the assertion besides `sub`. Defaults to " +
					"`[\"email\", \"preferred_username\"]`.",
				Validators: []validator.List{
					listvalidator.ValueStringsAre(stringvalidator.OneOf(gatewayAssertionClaims...)),
				},
			},
			"idle_timeout_seconds": schema.Int64Attribute{
				Optional:            true,
				Computed:            true,
				Default:             int64default.StaticInt64(1800),
				MarkdownDescription: "Sign the user out after this long without a request. 60 to 86400; defaults to 1800.",
				Validators:          []validator.Int64{int64validator.Between(60, 86400)},
			},
			"max_session_seconds": schema.Int64Attribute{
				Optional:            true,
				Computed:            true,
				Default:             int64default.StaticInt64(28800),
				MarkdownDescription: "Sign the user out this long after they signed in. 300 to 604800; defaults to 28800.",
				Validators:          []validator.Int64{int64validator.Between(300, 604800)},
			},
			"post_logout_redirect_uri": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "HTTPS URL to send users to after they sign out at " +
					"`/_p2/logout`. Without it they see a Phase Two page.",
				Validators: []validator.String{stringvalidator.RegexMatches(
					regexp.MustCompile(`^https://`), "must be an https URL")},
			},

			"origin_secret": schema.StringAttribute{
				Computed:  true,
				Sensitive: true,
				MarkdownDescription: "The value your origin must require in " +
					"`origin_secret_header`. Only known for an app this configuration created.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"origin_secret_header": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Header that carries the origin secret: `X-Phasetwo-Origin-Secret`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"status": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "`PENDING` until first activated, `ACTIVE` while serving, " +
					"`DISABLED` after deactivation.",
			},
			"client_id": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "The OIDC client created in the realm, `gateway-<public_hostname>`. " +
					"Client roles for `client_roles_any` are defined on it.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"issuer": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "OIDC issuer of the realm.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"callback_url": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Redirect URI registered on the client.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"assertion_issuer": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "`iss` of identity assertions.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"jwks_url": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Public keys that verify identity assertions.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"dns_target": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Create a CNAME from `public_hostname` to this.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *gatewayAppResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

func (r *gatewayAppResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan gatewayAppModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, diags := plan.request(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	created, err := r.client.CreateGatewayApp(ctx, plan.RealmID.ValueString(), body)
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not create the Gateway app", err))
		return
	}
	plan.ID = types.StringValue(created.App.Id)
	plan.OriginSecret = types.StringValue(created.OriginSecret)
	plan.DNSTarget = types.StringNull()
	resp.Diagnostics.Append(plan.setComputed(ctx, &created.App)...)
	// Saved before the detail read so a failure there cannot lose the secret.
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	app, err := r.client.GetGatewayApp(ctx, plan.RealmID.ValueString(), created.App.Id)
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not read the new Gateway app", err))
		return
	}
	resp.Diagnostics.Append(plan.setComputed(ctx, app)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *gatewayAppResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state gatewayAppModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	app, err := r.client.GetGatewayApp(ctx, state.RealmID.ValueString(), state.ID.ValueString())
	if err != nil {
		if api.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not read the Gateway app", err))
		return
	}
	resp.Diagnostics.Append(state.setFromAPI(ctx, app)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *gatewayAppResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state gatewayAppModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, diags := plan.request(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	plan.ID = state.ID
	plan.OriginSecret = state.OriginSecret
	app, err := r.client.UpdateGatewayApp(ctx, plan.RealmID.ValueString(), plan.ID.ValueString(), body)
	if err != nil {
		summary := "Could not update the Gateway app"
		if api.IsConflict(err) && plan.OriginURL.ValueString()+plan.HealthPath.ValueString() !=
			state.OriginURL.ValueString()+state.HealthPath.ValueString() {
			resp.Diagnostics.AddError(summary, fmt.Sprintf("%s\n\nAn active app cannot move its "+
				"origin. Remove its phasetwo_gateway_app_activation, apply, then change "+
				"origin_url or health_path and add the activation back.", err))
			return
		}
		resp.Diagnostics.Append(apiErrorDiagnostic(summary, err))
		return
	}
	// The update response has no readiness, so dns_target keeps its state value.
	resp.Diagnostics.Append(plan.setComputed(ctx, app)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *gatewayAppResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state gatewayAppModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteGatewayApp(ctx, state.RealmID.ValueString(), state.ID.ValueString()); err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not delete the Gateway app", err))
	}
}

// ImportState takes `realm_id/app_id`. The origin secret cannot be recovered.
func (r *gatewayAppResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	realmID, appID, ok := strings.Cut(req.ID, "/")
	if !ok || realmID == "" || appID == "" {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("Expected %q, got %q", "<realm_id>/<app_id>", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("realm_id"), realmID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), appID)...)
}

func stringList(ctx context.Context, l types.List, diags *diag.Diagnostics) *[]string {
	if l.IsNull() || l.IsUnknown() {
		return nil
	}
	var out []string
	diags.Append(l.ElementsAs(ctx, &out, false)...)
	return &out
}

func listOrNull(ctx context.Context, s *[]string, diags *diag.Diagnostics) types.List {
	if s == nil {
		return types.ListNull(types.StringType)
	}
	l, d := types.ListValueFrom(ctx, types.StringType, *s)
	diags.Append(d...)
	return l
}

// request is the full desired state. The API treats absent fields as unchanged, so cleared
// settings are sent as their empty form rather than omitted.
func (m gatewayAppModel) request(ctx context.Context) (client.ProtectedAppRequest, diag.Diagnostics) {
	var diags diag.Diagnostics
	body := client.ProtectedAppRequest{
		Name:           m.Name.ValueString(),
		PublicHostname: m.PublicHostname.ValueString(),
		OriginUrl:      m.OriginURL.ValueString(),
	}
	healthPath := m.HealthPath.ValueString()
	body.HealthPath = &healthPath
	enabled := m.AssertionEnabled.ValueBool()
	body.AssertionEnabled = &enabled
	body.AssertionClaims = stringList(ctx, m.AssertionClaims, &diags)
	idle, maxSession := int32(m.IdleTimeoutSeconds.ValueInt64()), int32(m.MaxSessionSeconds.ValueInt64())
	body.IdleTimeoutSeconds, body.MaxSessionSeconds = &idle, &maxSession
	postLogout := m.PostLogoutRedirectURI.ValueString()
	body.PostLogoutRedirectUri = &postLogout

	headers := map[string]string{}
	if !m.HeaderMappings.IsNull() && !m.HeaderMappings.IsUnknown() {
		diags.Append(m.HeaderMappings.ElementsAs(ctx, &headers, false)...)
	}
	body.HeaderMappings = &headers

	var pm gatewayPolicyModel
	diags.Append(m.Policy.As(ctx, &pm, basetypes.ObjectAsOptions{})...)
	def := client.ProtectedAppPolicyDefault(pm.Default.ValueString())
	rules := make([]client.ProtectedAppPolicyRule, 0, len(pm.Rules))
	for i, rm := range pm.Rules {
		rule := client.ProtectedAppPolicyRule{
			PathPrefix:     rm.PathPrefix.ValueString(),
			Methods:        stringList(ctx, rm.Methods, &diags),
			RealmRolesAny:  stringList(ctx, rm.RealmRolesAny, &diags),
			ClientRolesAny: stringList(ctx, rm.ClientRolesAny, &diags),
			GroupsAny:      stringList(ctx, rm.GroupsAny, &diags),
		}
		if !rm.Effect.IsNull() {
			e := client.ProtectedAppPolicyRuleEffect(rm.Effect.ValueString())
			rule.Effect = &e
		}
		if !rm.ClaimsEqual.IsNull() {
			var raw map[string]string
			diags.Append(rm.ClaimsEqual.ElementsAs(ctx, &raw, false)...)
			claims := map[string]interface{}{}
			for k, v := range raw {
				if !gatewayBooleanClaims[k] {
					claims[k] = v
					continue
				}
				b, err := strconv.ParseBool(v)
				if err != nil {
					diags.AddAttributeError(
						path.Root("policy").AtName("rules").AtListIndex(i).AtName("claims_equal").AtMapKey(k),
						"Invalid claim value", fmt.Sprintf("%s is a boolean claim; use \"true\" or \"false\"", k))
					continue
				}
				claims[k] = b
			}
			rule.ClaimsEqual = &claims
		}
		rules = append(rules, rule)
	}
	body.Policy = &client.ProtectedAppPolicy{Default: &def, Rules: &rules}
	return body, diags
}

// setComputed fills the attributes only the API decides.
func (m *gatewayAppModel) setComputed(_ context.Context, app *client.ProtectedApp) diag.Diagnostics {
	m.ID = types.StringValue(app.Id)
	m.Status = types.StringValue(string(app.Status))
	m.ClientID = types.StringValue(app.ClientId)
	m.Issuer = stringOrNull(app.Issuer)
	m.CallbackURL = types.StringValue(app.CallbackUrl)
	m.AssertionIssuer = stringOrNull(app.AssertionIssuer)
	m.JWKSURL = stringOrNull(app.JwksUrl)
	m.OriginSecretHeader = types.StringValue(app.OriginSecretHeader)
	if app.Readiness != nil && app.Readiness.Hostname != nil {
		m.DNSTarget = stringOrNull(app.Readiness.Hostname.DnsTarget)
	}
	return nil
}

// setFromAPI refreshes every attribute, so changes made in the dashboard show up as drift.
func (m *gatewayAppModel) setFromAPI(ctx context.Context, app *client.ProtectedApp) diag.Diagnostics {
	var diags diag.Diagnostics
	diags.Append(m.setComputed(ctx, app)...)
	m.RealmID = types.StringValue(app.DeploymentId)
	m.Name = types.StringValue(app.Name)
	m.PublicHostname = types.StringValue(app.PublicHostname)
	m.OriginURL = types.StringValue(app.OriginUrl)
	m.HealthPath = types.StringValue(app.HealthPath)
	m.AssertionEnabled = types.BoolValue(app.AssertionEnabled)
	m.AssertionClaims = listOrNull(ctx, app.AssertionClaims, &diags)
	if m.AssertionClaims.IsNull() {
		m.AssertionClaims = types.ListValueMust(types.StringType, nil)
	}
	m.IdleTimeoutSeconds = types.Int64Value(int64(app.IdleTimeoutSeconds))
	m.MaxSessionSeconds = types.Int64Value(int64(app.MaxSessionSeconds))
	m.PostLogoutRedirectURI = stringOrNull(app.PostLogoutRedirectUri)

	headers := map[string]string{}
	if app.HeaderMappings != nil {
		headers = *app.HeaderMappings
	}
	hm, d := types.MapValueFrom(ctx, types.StringType, headers)
	diags.Append(d...)
	m.HeaderMappings = hm

	pm := gatewayPolicyModel{Default: types.StringValue("authenticated"), Rules: []gatewayRuleModel{}}
	if app.Policy.Default != nil {
		pm.Default = types.StringValue(string(*app.Policy.Default))
	}
	if app.Policy.Rules != nil {
		for _, rule := range *app.Policy.Rules {
			rm := gatewayRuleModel{
				PathPrefix:     types.StringValue(rule.PathPrefix),
				Methods:        listOrNull(ctx, rule.Methods, &diags),
				Effect:         types.StringNull(),
				RealmRolesAny:  listOrNull(ctx, rule.RealmRolesAny, &diags),
				ClientRolesAny: listOrNull(ctx, rule.ClientRolesAny, &diags),
				GroupsAny:      listOrNull(ctx, rule.GroupsAny, &diags),
				ClaimsEqual:    types.MapNull(types.StringType),
			}
			if rule.Effect != nil {
				rm.Effect = types.StringValue(string(*rule.Effect))
			}
			if rule.ClaimsEqual != nil {
				claims := map[string]string{}
				for k, v := range *rule.ClaimsEqual {
					claims[k] = fmt.Sprint(v)
				}
				ce, d := types.MapValueFrom(ctx, types.StringType, claims)
				diags.Append(d...)
				rm.ClaimsEqual = ce
			}
			pm.Rules = append(pm.Rules, rm)
		}
	}
	policy, d := types.ObjectValueFrom(ctx, gatewayPolicyAttrTypes, pm)
	diags.Append(d...)
	m.Policy = policy
	return diags
}
