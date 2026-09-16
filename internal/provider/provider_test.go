package provider

import (
	"context"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

// testAccProtoV6ProviderFactories wires the in-process provider into the acceptance test
// harness, so tests exercise the real plugin rather than a stub.
var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"phasetwo": providerserver.NewProtocol6WithError(New("test")()),
}

// Acceptance tests create real, billable infrastructure. They need credentials plus a test
// organization that already has a saved payment method, since the provider cannot add one.
func testAccPreCheck(t *testing.T) {
	t.Helper()
	required := []string{
		"PHASETWO_CLIENT_ID",
		"PHASETWO_CLIENT_SECRET",
		"PHASETWO_TEST_ORG_ID",
		"PHASETWO_TEST_PAYMENT_METHOD_ID",
	}
	for _, k := range required {
		if os.Getenv(k) == "" {
			t.Fatalf("%s must be set for acceptance tests", k)
		}
	}
	if os.Getenv("PHASETWO_ENVIRONMENT") == "app" {
		t.Fatal("refusing to run acceptance tests against production; " +
			"set PHASETWO_ENVIRONMENT=app-staging")
	}
}

// GetProviderSchema is the first call Terraform makes, and it runs the framework's full schema
// validation. Asking the protocol server for it is the cheapest way to catch the mistakes that
// otherwise only appear when someone runs `terraform plan`: an attribute that is neither
// required nor optional nor computed, a default on a non-computed attribute, a bad nested block.
func TestGetProviderSchema(t *testing.T) {
	ctx := context.Background()

	srv, err := providerserver.NewProtocol6WithError(New("test")())()
	if err != nil {
		t.Fatalf("building the provider server: %v", err)
	}

	resp, err := srv.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatalf("GetProviderSchema: %v", err)
	}
	for _, d := range resp.Diagnostics {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			t.Errorf("schema error: %s: %s", d.Summary, d.Detail)
		}
	}

	wantResources := []string{
		"phasetwo_cluster",
		"phasetwo_cluster_domain",
		"phasetwo_cluster_environment_variable",
		"phasetwo_cluster_extension",
		"phasetwo_cluster_extension_version",
		"phasetwo_cluster_ip_rules",
		"phasetwo_cluster_primary_host",
		"phasetwo_realm",
		"phasetwo_realm_credential",
	}
	for _, name := range wantResources {
		if _, ok := resp.ResourceSchemas[name]; !ok {
			t.Errorf("resource %s is not registered", name)
		}
	}
	if got, want := len(resp.ResourceSchemas), len(wantResources); got != want {
		t.Errorf("registered %d resources, want %d — a new one may be unregistered", got, want)
	}

	wantDataSources := []string{
		"phasetwo_cluster",
		"phasetwo_organization",
		"phasetwo_organizations",
		"phasetwo_payment_method",
		"phasetwo_realm",
		"phasetwo_regions",
	}
	for _, name := range wantDataSources {
		if _, ok := resp.DataSourceSchemas[name]; !ok {
			t.Errorf("data source %s is not registered", name)
		}
	}
	if got, want := len(resp.DataSourceSchemas), len(wantDataSources); got != want {
		t.Errorf("registered %d data sources, want %d", got, want)
	}

	// Ephemeral resources are registered through a separate provider interface, so a resource
	// added to the wrong list is registered nowhere and fails only at apply time.
	wantEphemeral := []string{
		"phasetwo_realm_credential_secret",
	}
	for _, name := range wantEphemeral {
		if _, ok := resp.EphemeralResourceSchemas[name]; !ok {
			t.Errorf("ephemeral resource %s is not registered", name)
		}
	}
	if got, want := len(resp.EphemeralResourceSchemas), len(wantEphemeral); got != want {
		t.Errorf("registered %d ephemeral resources, want %d", got, want)
	}

	// The secret must not be reachable as a resource attribute: that is the whole reason the
	// ephemeral resource exists, and a well-meaning addition of `client_secret` to the resource
	// would silently put a realm-admin credential back into terraform.tfstate.
	if credential, ok := resp.ResourceSchemas["phasetwo_realm_credential"]; ok {
		for _, attr := range credential.Block.Attributes {
			if attr.Name == "client_secret" {
				t.Error("phasetwo_realm_credential must not expose client_secret; " +
					"Terraform writes attributes to state — use the ephemeral resource")
			}
		}
	}
}

// Credentials are the one bit of provider configuration with no sensible default, so a missing
// pair has to produce a clear error rather than a confusing 401 on the first API call.
func TestConfigureRequiresCredentials(t *testing.T) {
	for _, k := range []string{
		"PHASETWO_CLIENT_ID", "PHASETWO_CLIENT_SECRET", "PHASETWO_ACCESS_TOKEN",
	} {
		t.Setenv(k, "")
	}

	ctx := context.Background()
	srv, err := providerserver.NewProtocol6WithError(New("test")())()
	if err != nil {
		t.Fatalf("building the provider server: %v", err)
	}

	schemaResp, err := srv.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatalf("GetProviderSchema: %v", err)
	}

	cfg, err := emptyProviderConfig(schemaResp)
	if err != nil {
		t.Fatalf("building an empty config: %v", err)
	}

	resp, err := srv.ConfigureProvider(ctx, &tfprotov6.ConfigureProviderRequest{Config: cfg})
	if err != nil {
		t.Fatalf("ConfigureProvider: %v", err)
	}

	var found bool
	for _, d := range resp.Diagnostics {
		if d.Severity == tfprotov6.DiagnosticSeverityError &&
			d.Summary == "Missing Phase Two API credentials" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a credentials error, got diagnostics: %+v", resp.Diagnostics)
	}
}
