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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/p2-inc/terraform-provider-phasetwo/internal/api"
	"github.com/p2-inc/terraform-provider-phasetwo/internal/client"
)

const (
	defaultDomainCreateTimeout = 60 * time.Minute
	domainPollInterval         = 30 * time.Second
)

var (
	_ resource.Resource                = (*clusterDomainResource)(nil)
	_ resource.ResourceWithConfigure   = (*clusterDomainResource)(nil)
	_ resource.ResourceWithImportState = (*clusterDomainResource)(nil)
)

// NewClusterDomainResource returns the phasetwo_cluster_domain resource.
func NewClusterDomainResource() resource.Resource { return &clusterDomainResource{} }

type clusterDomainResource struct {
	client *api.Client
}

type domainRecordModel struct {
	Type  types.String `tfsdk:"type"`
	Name  types.String `tfsdk:"name"`
	Value types.String `tfsdk:"value"`
}

type clusterDomainModel struct {
	ID                 types.String `tfsdk:"id"`
	ClusterID          types.String `tfsdk:"cluster_id"`
	Host               types.String `tfsdk:"host"`
	WaitForCertificate types.Bool   `tfsdk:"wait_for_certificate"`

	Type              types.String        `tfsdk:"type"`
	Status            types.String        `tfsdk:"status"`
	CertificateStatus types.String        `tfsdk:"certificate_status"`
	DomainRecords     []domainRecordModel `tfsdk:"domain_records"`
	OrganizationID    types.String        `tfsdk:"organization_id"`
	CreatedAt         types.String        `tfsdk:"created_at"`

	Timeouts timeouts.Value `tfsdk:"timeouts"`
}

func (r *clusterDomainResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cluster_domain"
}

func (r *clusterDomainResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A custom hostname on a dedicated cluster.\n\n" +
			"Creating this registers the hostname and starts DNS validation. It does **not** " +
			"create DNS records for you — read `domain_records` and create them at your DNS " +
			"provider, after which Phase Two validates the domain and issues a certificate.\n\n" +
			"Adding a domain does not start serving traffic on it. Once its certificate is " +
			"issued, point `phasetwo_cluster_primary_host` at it.\n\n" +
			"~> Part of domain provisioning can involve a manual step on the Phase Two side, so " +
			"`wait_for_certificate` is off by default. Turning it on can block an apply for a " +
			"long time.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Unique identifier of the custom domain.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"cluster_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "ID of the cluster. The cluster must be `ACTIVE`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"host": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The custom hostname, e.g. `auth.example.com`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"wait_for_certificate": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				MarkdownDescription: "Block until the TLS certificate is issued. Off by default, " +
					"because issuance waits on DNS records that Terraform has not created yet — " +
					"turning it on in the same apply that creates those records will deadlock. " +
					"Use it in a later apply, or with a `depends_on` covering your DNS resources.",
			},

			"type": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "What the hostname is used for: `WEB` or `MAIL`.",
			},
			"status": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "DNS validation status: `PENDING_VALIDATION`, `SUCCESS`, " +
					"`FAILED` or `NOT_FOUND`.",
			},
			"certificate_status": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "TLS certificate status. The domain can serve traffic once " +
					"this is `ISSUED`.",
			},
			"domain_records": schema.ListNestedAttribute{
				Computed: true,
				MarkdownDescription: "DNS records to create in order to validate the domain. Empty " +
					"once validation has completed.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"type": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "Record type, e.g. `CNAME`.",
						},
						"name": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "Record name.",
						},
						"value": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "Record value.",
						},
					},
				},
			},
			"organization_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "ID of the organization that owns the cluster.",
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "When the domain was added, as an RFC 3339 timestamp.",
			},

			"timeouts": timeouts.AttributesAll(ctx),
		},
	}
}

func (r *clusterDomainResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

func (r *clusterDomainResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan clusterDomainModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	configured, diags := plan.Timeouts.Create(ctx, defaultDomainCreateTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout := defaultTimeout(configured, defaultDomainCreateTimeout)
	ctx, cancel := ctxWithTimeout(ctx, timeout)
	defer cancel()

	clusterID := plan.ClusterID.ValueString()
	validation, err := r.client.CreateDomain(ctx, clusterID, plan.Host.ValueString())
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not add the custom domain", err))
		return
	}

	r.applyValidation(&plan, validation)
	resp.State.Set(ctx, &plan)

	if plan.WaitForCertificate.ValueBool() {
		final, err := r.waitForCertificate(ctx, clusterID, validation.Domain.Id, timeout)
		if err != nil {
			resp.Diagnostics.Append(apiErrorDiagnostic("Certificate was not issued", err))
			return
		}
		r.applyValidation(&plan, final)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *clusterDomainResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state clusterDomainModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	clusterID := state.ClusterID.ValueString()
	validation, err := r.client.GetDomainStatus(ctx, clusterID, state.ID.ValueString())
	if err != nil {
		if api.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not read the custom domain", err))
		return
	}

	r.applyValidation(&state, validation)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update handles wait_for_certificate flipping to true; host and cluster_id force replacement.
func (r *clusterDomainResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state clusterDomainModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	plan.ID = state.ID
	clusterID := plan.ClusterID.ValueString()

	configured, diags := plan.Timeouts.Update(ctx, defaultDomainCreateTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout := defaultTimeout(configured, defaultDomainCreateTimeout)
	ctx, cancel := ctxWithTimeout(ctx, timeout)
	defer cancel()

	if plan.WaitForCertificate.ValueBool() {
		final, err := r.waitForCertificate(ctx, clusterID, plan.ID.ValueString(), timeout)
		if err != nil {
			resp.Diagnostics.Append(apiErrorDiagnostic("Certificate was not issued", err))
			return
		}
		r.applyValidation(&plan, final)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}

	validation, err := r.client.GetDomainStatus(ctx, clusterID, plan.ID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(apiErrorDiagnostic("Could not read the custom domain", err))
		return
	}
	r.applyValidation(&plan, validation)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *clusterDomainResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state clusterDomainModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.DeleteDomain(ctx, state.ClusterID.ValueString(), state.ID.ValueString())
	if err != nil {
		if api.IsNotFound(err) {
			return
		}
		summary := "Could not remove the custom domain"
		if api.IsConflict(err) {
			resp.Diagnostics.AddError(summary,
				fmt.Sprintf("%s\n\nA domain currently serving as the cluster's primary hostname "+
					"cannot be removed. Point phasetwo_cluster_primary_host at another domain "+
					"first — Terraform will not reorder these for you, so a depends_on may be "+
					"needed.", err))
			return
		}
		resp.Diagnostics.Append(apiErrorDiagnostic(summary, err))
	}
}

// ImportState takes `cluster_id/domain_id`, since a domain is only addressable within a cluster.
func (r *clusterDomainResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("Expected %q, got %q", "<cluster_id>/<domain_id>", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cluster_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
}

// waitForCertificate polls until the certificate is issued or reaches a state it will not leave.
func (r *clusterDomainResource) waitForCertificate(
	ctx context.Context, clusterID, domainID string, timeout time.Duration,
) (*client.CustomerDomainValidation, error) {
	deadline := time.Now().Add(timeout)
	for {
		v, err := r.client.GetDomainStatus(ctx, clusterID, domainID)
		if err != nil {
			return nil, err
		}
		switch v.CertificateStatus {
		case api.CertIssued:
			return v, nil
		case api.CertFailed, api.CertTimedOut, api.CertRevoked, api.CertExpired:
			return v, fmt.Errorf("certificate for domain %s is in %s (DNS validation: %s); "+
				"check that the records in domain_records exist and are correct",
				domainID, v.CertificateStatus, v.Status)
		}

		if time.Now().After(deadline) {
			return v, fmt.Errorf("timed out after %s waiting for the certificate on domain %s "+
				"(certificate %s, DNS validation %s); this usually means the DNS records in "+
				"domain_records have not been created yet",
				timeout, domainID, v.CertificateStatus, v.Status)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(domainPollInterval):
		}
	}
}

func (r *clusterDomainResource) applyValidation(m *clusterDomainModel, v *client.CustomerDomainValidation) {
	m.Status = types.StringValue(string(v.Status))
	m.CertificateStatus = types.StringValue(string(v.CertificateStatus))

	d := v.Domain
	m.ID = types.StringValue(d.Id)
	m.Host = types.StringValue(d.Domain)
	m.Type = types.StringValue(string(d.Type))
	m.OrganizationID = stringOrNull(d.OrganizationId)
	m.CreatedAt = epochMillisPtrToRFC3339(d.CreatedAt)

	m.DomainRecords = nil
	if d.DomainRecords != nil {
		for _, rec := range *d.DomainRecords {
			m.DomainRecords = append(m.DomainRecords, domainRecordModel{
				Type:  types.StringValue(string(rec.Type)),
				Name:  types.StringValue(rec.Name),
				Value: types.StringValue(rec.Value),
			})
		}
	}
	if m.DomainRecords == nil {
		m.DomainRecords = []domainRecordModel{}
	}
}
