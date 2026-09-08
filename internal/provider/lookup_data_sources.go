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
	"github.com/p2-inc/terraform-provider-phasetwo/internal/client"
)

// ---------------------------------------------------------------------------
// phasetwo_cluster
// ---------------------------------------------------------------------------

var (
	_ datasource.DataSource              = (*clusterDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*clusterDataSource)(nil)
)

// NewClusterDataSource returns the phasetwo_cluster data source.
func NewClusterDataSource() datasource.DataSource { return &clusterDataSource{} }

type clusterDataSource struct{ client *api.Client }

type clusterDataSourceModel struct {
	ID             types.String `tfsdk:"id"`
	Name           types.String `tfsdk:"name"`
	Host           types.String `tfsdk:"host"`
	Region         types.String `tfsdk:"region"`
	Status         types.String `tfsdk:"status"`
	Tier           types.String `tfsdk:"tier"`
	Owner          types.String `tfsdk:"owner"`
	Variant        types.String `tfsdk:"variant"`
	Domain         types.String `tfsdk:"domain"`
	ResourceLimits types.String `tfsdk:"resource_limits"`
	CreatedAt      types.String `tfsdk:"created_at"`
}

func (d *clusterDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cluster"
}

func (d *clusterDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An existing dedicated cluster.\n\n" +
			"Give exactly one of `id` or `name`. Archived clusters are not returned.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "ID of the cluster. Set this or `name`.",
			},
			"name": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Name of the cluster. Set this or `id`.",
			},
			"host":            schema.StringAttribute{Computed: true, MarkdownDescription: "Base URL of the cluster's Keycloak instance."},
			"region":          schema.StringAttribute{Computed: true, MarkdownDescription: "Region the cluster runs in."},
			"status":          schema.StringAttribute{Computed: true, MarkdownDescription: "Lifecycle state."},
			"tier":            schema.StringAttribute{Computed: true, MarkdownDescription: "Subscription tier."},
			"owner":           schema.StringAttribute{Computed: true, MarkdownDescription: "ID of the owning organization."},
			"variant":         schema.StringAttribute{Computed: true, MarkdownDescription: "`DEDICATED` or `SHARED`."},
			"domain":          schema.StringAttribute{Computed: true, MarkdownDescription: "Custom domain recorded at provision time."},
			"resource_limits": schema.StringAttribute{Computed: true, MarkdownDescription: "`standard` or `custom`."},
			"created_at":      schema.StringAttribute{Computed: true, MarkdownDescription: "Creation time, as an RFC 3339 timestamp."},
		},
	}
}

func (d *clusterDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *clusterDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg clusterDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	wantID := cfg.ID.ValueString()
	wantName := cfg.Name.ValueString()
	if (wantID == "") == (wantName == "") {
		resp.Diagnostics.AddError("Specify exactly one of id or name",
			"Look up a cluster either by id or by name, not both and not neither.")
		return
	}

	var found *client.Cluster
	if wantID != "" {
		c, err := d.client.GetCluster(ctx, wantID)
		if err != nil {
			if api.IsNotFound(err) {
				resp.Diagnostics.AddError("No such cluster",
					fmt.Sprintf("No cluster with id %s is visible to this API secret.", wantID))
				return
			}
			resp.Diagnostics.Append(apiErrorDiagnostic("Could not read the cluster", err))
			return
		}
		found = c
	} else {
		clusters, err := d.client.ListClusters(ctx)
		if err != nil {
			resp.Diagnostics.Append(apiErrorDiagnostic("Could not list clusters", err))
			return
		}
		names := make([]string, 0, len(clusters))
		for i := range clusters {
			names = append(names, clusters[i].Name)
			if clusters[i].Name == wantName {
				found = &clusters[i]
				break
			}
		}
		if found == nil {
			sort.Strings(names)
			resp.Diagnostics.AddError("No such cluster",
				fmt.Sprintf("No cluster named %q is visible to this API secret.\n\nVisible "+
					"clusters: %s", wantName, strings.Join(names, ", ")))
			return
		}
	}

	cfg.ID = types.StringValue(found.Id)
	cfg.Name = types.StringValue(found.Name)
	cfg.Host = types.StringValue(found.Host)
	cfg.Region = types.StringValue(found.Region.Name)
	cfg.Status = types.StringValue(string(found.Status))
	cfg.Tier = types.StringValue(string(found.Tier))
	cfg.ResourceLimits = types.StringValue(string(found.ResourceLimits))
	cfg.Owner = stringOrNull(found.Owner)
	cfg.Variant = stringOrNull(found.Variant)
	cfg.Domain = stringOrNull(found.Domain)
	cfg.CreatedAt = epochMillisToRFC3339(found.CreatedAt)
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}

// ---------------------------------------------------------------------------
// phasetwo_realm
// ---------------------------------------------------------------------------

var (
	_ datasource.DataSource              = (*realmDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*realmDataSource)(nil)
)

// NewRealmDataSource returns the phasetwo_realm data source.
func NewRealmDataSource() datasource.DataSource { return &realmDataSource{} }

type realmDataSource struct{ client *api.Client }

type realmDataSourceModel struct {
	ID          types.String `tfsdk:"id"`
	ClusterID   types.String `tfsdk:"cluster_id"`
	Name        types.String `tfsdk:"name"`
	DisplayName types.String `tfsdk:"display_name"`
	State       types.String `tfsdk:"state"`
	StateError  types.String `tfsdk:"state_error"`
	Region      types.String `tfsdk:"region"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

func (d *realmDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_realm"
}

func (d *realmDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An existing realm on a dedicated cluster.\n\n" +
			"Give either `id`, or `cluster_id` together with `name`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "ID of the realm. Set this, or `cluster_id` and `name`.",
			},
			"cluster_id": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "ID of the cluster the realm runs on. Required with `name`.",
			},
			"name": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Name of the realm. Requires `cluster_id`.",
			},
			"display_name": schema.StringAttribute{Computed: true, MarkdownDescription: "Human-readable name."},
			"state":        schema.StringAttribute{Computed: true, MarkdownDescription: "Lifecycle state."},
			"state_error":  schema.StringAttribute{Computed: true, MarkdownDescription: "Why the realm last failed."},
			"region":       schema.StringAttribute{Computed: true, MarkdownDescription: "Region of the hosting cluster."},
			"created_at":   schema.StringAttribute{Computed: true, MarkdownDescription: "Creation time, as an RFC 3339 timestamp."},
		},
	}
}

func (d *realmDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *realmDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg realmDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	wantID := cfg.ID.ValueString()
	wantName := cfg.Name.ValueString()
	wantCluster := cfg.ClusterID.ValueString()

	switch {
	case wantID != "" && wantName != "":
		resp.Diagnostics.AddError("Specify id or name, not both",
			"Look up a realm either by id, or by cluster_id and name.")
		return
	case wantID == "" && wantName == "":
		resp.Diagnostics.AddError("Specify id, or cluster_id and name",
			"Look up a realm either by id, or by cluster_id and name.")
		return
	case wantID == "" && wantCluster == "":
		resp.Diagnostics.AddError("cluster_id is required with name",
			"Realm names are unique within a cluster, so a name alone is ambiguous.")
		return
	}

	var found *client.Deployment
	if wantID != "" {
		dep, err := d.client.GetRealm(ctx, wantID)
		if err != nil {
			if api.IsNotFound(err) {
				resp.Diagnostics.AddError("No such realm",
					fmt.Sprintf("No realm with id %s is visible to this API secret.", wantID))
				return
			}
			resp.Diagnostics.Append(apiErrorDiagnostic("Could not read the realm", err))
			return
		}
		found = dep
	} else {
		// Names are lowercased on create, so match the lowered form.
		lowered := strings.ToLower(wantName)
		realms, err := d.client.ListRealms(ctx, wantCluster, &lowered)
		if err != nil {
			resp.Diagnostics.Append(apiErrorDiagnostic("Could not list realms", err))
			return
		}
		names := make([]string, 0, len(realms))
		for i := range realms {
			names = append(names, realms[i].Name)
			if realms[i].Name == lowered {
				found = &realms[i]
				break
			}
		}
		if found == nil {
			sort.Strings(names)
			resp.Diagnostics.AddError("No such realm",
				fmt.Sprintf("No realm named %q on cluster %s.\n\nRealms matching that search: %s",
					lowered, wantCluster, strings.Join(names, ", ")))
			return
		}
	}

	cfg.ID = types.StringValue(found.Id)
	cfg.Name = types.StringValue(found.Name)
	cfg.State = types.StringValue(string(found.State))
	cfg.DisplayName = stringOrNull(found.DisplayName)
	cfg.StateError = stringOrNull(found.StateError)
	cfg.Region = stringOrNull(found.Region)
	cfg.CreatedAt = epochMillisPtrToRFC3339(found.CreatedAt)
	if found.Cluster != nil {
		cfg.ClusterID = types.StringValue(found.Cluster.Id)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
