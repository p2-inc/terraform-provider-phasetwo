package provider

import (
	"context"
	"errors"
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

const defaultGatewayActivationTimeout = 30 * time.Minute

// gatewayPollInterval is a variable so tests do not wait for real.
var gatewayPollInterval = 15 * time.Second

var (
	_ resource.Resource                = (*gatewayAppActivationResource)(nil)
	_ resource.ResourceWithConfigure   = (*gatewayAppActivationResource)(nil)
	_ resource.ResourceWithImportState = (*gatewayAppActivationResource)(nil)
)

// NewGatewayAppActivationResource returns the phasetwo_gateway_app_activation resource.
func NewGatewayAppActivationResource() resource.Resource {
	return &gatewayAppActivationResource{}
}

type gatewayAppActivationResource struct {
	client *api.Client
}

type gatewayAppActivationModel struct {
	ID                 types.String   `tfsdk:"id"`
	RealmID            types.String   `tfsdk:"realm_id"`
	AppID              types.String   `tfsdk:"app_id"`
	VerificationResult types.String   `tfsdk:"verification_result"`
	VerifiedAt         types.String   `tfsdk:"verified_at"`
	Timeouts           timeouts.Value `tfsdk:"timeouts"`
}

func (r *gatewayAppActivationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_gateway_app_activation"
}

func (r *gatewayAppActivationResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Starts serving a [`phasetwo_gateway_app`](gateway_app). Separate " +
			"from the app because it has to wait for things you configure using the app's " +
			"outputs: the origin requiring `origin_secret`, and the CNAME to `dns_target`. " +
			"Reference those resources (or use `depends_on`) so Terraform creates them first.\n\n" +
			"Create verifies the origin, then waits for the hostname and certificate and " +
			"activates the app. Verification sends four requests to the app's `health_path`: " +
			"with the secret, without it, with a wrong one and with forged identity headers. " +
			"It passes only if the first gets a 2xx and the other three a 401 or 403. An origin " +
			"that serves a request without the secret fails the apply immediately; anything " +
			"inconclusive (a 404, a redirect, a timeout) is retried until the timeout.\n\n" +
			"Destroying this deactivates the app: the Gateway refuses its traffic at once and " +
			"ends every session, and the app keeps its configuration.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Same as `app_id`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"realm_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "ID of the app's realm.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"app_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "ID of the `phasetwo_gateway_app`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"verification_result": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Result of the verification that allowed activation: `VERIFIED`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"verified_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "When that verification ran.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{Create: true}),
		},
	}
}

func (r *gatewayAppActivationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

// errOriginAdmitsUnauthenticated is not retried: waiting will not make the origin require the
// secret, and it means the website is reachable without signing in.
var errOriginAdmitsUnauthenticated = errors.New("origin admits requests without the secret")

func (r *gatewayAppActivationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan gatewayAppActivationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	configured, diags := plan.Timeouts.Create(ctx, defaultGatewayActivationTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout := defaultTimeout(configured, defaultGatewayActivationTimeout)
	ctx, cancel := ctxWithTimeout(ctx, timeout)
	defer cancel()

	app, err := r.activate(ctx, plan.RealmID.ValueString(), plan.AppID.ValueString(), timeout)
	if err != nil {
		resp.Diagnostics.AddError("Could not activate the Gateway app", err.Error())
		return
	}
	plan.ID = types.StringValue(app.Id)
	plan.set(app)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// activate verifies until the origin passes, then activates once the hostname is ready.
func (r *gatewayAppActivationResource) activate(
	ctx context.Context, realmID, appID string, timeout time.Duration,
) (*client.ProtectedApp, error) {
	deadline := time.Now().Add(timeout)
	var last string
	for {
		app, err := r.client.GetGatewayApp(ctx, realmID, appID)
		if err != nil {
			return nil, err
		}
		if app.Status == api.GatewayAppActive {
			return app, nil
		}
		readiness := client.ProtectedAppReadiness{}
		if app.Readiness != nil {
			readiness = *app.Readiness
		}

		verified := readiness.OriginVerified != nil && *readiness.OriginVerified
		if !verified {
			verifiedApp, err := r.client.VerifyGatewayApp(ctx, realmID, appID)
			if err != nil {
				return nil, err
			}
			v := verifiedApp.Verification
			switch {
			case v != nil && v.Result == api.GatewayVerified:
				verified = true
			case v != nil && v.Result == "FAILED":
				return nil, fmt.Errorf("%w: %s.\n\nConfigure the origin to answer 401 or 403 "+
					"unless %s carries origin_secret, then apply again.\n\n%s",
					errOriginAdmitsUnauthenticated, deref(v.Reason), app.OriginSecretHeader, probes(v))
			default:
				last = "the origin verification was inconclusive"
				if v != nil {
					last += ": " + deref(v.Reason) + "\n\n" + probes(v)
				}
			}
		}

		if verified {
			if readiness.HostnameReady != nil && *readiness.HostnameReady {
				activated, err := r.client.ActivateGatewayApp(ctx, realmID, appID)
				if err == nil {
					return activated, nil
				}
				if !api.IsConflict(err) {
					return nil, err
				}
				last = err.Error()
			} else {
				last = "the hostname is not ready: " + hostnameState(app)
			}
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out after %s; %s", timeout, last)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%w; %s", ctx.Err(), last)
		case <-time.After(gatewayPollInterval):
		}
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func probes(v *client.ProtectedAppVerification) string {
	if v.Probes == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("Probes of " + deref(v.Url) + ":")
	for _, p := range *v.Probes {
		name := ""
		if p.Name != nil {
			name = string(*p.Name)
		}
		switch {
		case p.Status != nil:
			fmt.Fprintf(&b, "\n  %-16s %d", name, *p.Status)
		default:
			fmt.Fprintf(&b, "\n  %-16s %s", name, deref(p.Error))
		}
	}
	return b.String()
}

func hostnameState(app *client.ProtectedApp) string {
	if app.Readiness == nil || app.Readiness.Hostname == nil {
		return "status unknown"
	}
	h := app.Readiness.Hostname
	s := fmt.Sprintf("hostname %s, certificate %s", deref(h.Status), deref(h.SslStatus))
	if h.DnsOk != nil && !*h.DnsOk {
		s += fmt.Sprintf(", and %s is not a CNAME to %s yet", app.PublicHostname, deref(h.DnsTarget))
	}
	if h.Error != nil {
		s += " (" + *h.Error + ")"
	}
	return s
}

func (m *gatewayAppActivationModel) set(app *client.ProtectedApp) {
	m.VerificationResult = types.StringNull()
	m.VerifiedAt = types.StringNull()
	if app.Verification != nil {
		m.VerificationResult = types.StringValue(string(app.Verification.Result))
		m.VerifiedAt = stringOrNull(app.Verification.CheckedAt)
	}
}

// Read drops the activation when the app is no longer active, so the next apply activates it
// again.
func (r *gatewayAppActivationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state gatewayAppActivationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	app, err := r.client.GetGatewayApp(ctx, state.RealmID.ValueString(), state.AppID.ValueString())
	if err != nil {
		if api.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not read the Gateway app", err))
		return
	}
	if app.Status != api.GatewayAppActive {
		resp.State.RemoveResource(ctx)
		return
	}
	state.set(app)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is unreachable: both arguments force replacement.
func (r *gatewayAppActivationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan gatewayAppActivationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *gatewayAppActivationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state gatewayAppActivationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.DeactivateGatewayApp(ctx, state.RealmID.ValueString(), state.AppID.ValueString())
	if err != nil && !api.IsNotFound(err) {
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not deactivate the Gateway app", err))
	}
}

// ImportState takes `realm_id/app_id` of an active app.
func (r *gatewayAppActivationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	realmID, appID, ok := strings.Cut(req.ID, "/")
	if !ok || realmID == "" || appID == "" {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("Expected %q, got %q", "<realm_id>/<app_id>", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("realm_id"), realmID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("app_id"), appID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), appID)...)
}
