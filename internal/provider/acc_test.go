package provider

import (
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// Acceptance tests run only with TF_ACC set, and create real, billable infrastructure. See the
// README for the environment they need.
//
// Note that a destroyed cluster does not disappear: it moves to PENDING_DELETION and is torn
// down at the end of the billing cycle, keeping its name reserved. Every test therefore uses a
// unique name, and a run leaves clusters behind. Use a dedicated test organization.

func accClusterConfig(name string) string {
	return fmt.Sprintf(`
resource "phasetwo_cluster" "test" {
  name              = %[1]q
  region            = "US_EAST_1"
  tier              = "premium"
  organization_id   = %[2]q
  payment_method_id = %[3]q
}
`, name, os.Getenv("PHASETWO_TEST_ORG_ID"), os.Getenv("PHASETWO_TEST_PAYMENT_METHOD_ID"))
}

func TestAccCluster(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("set TF_ACC=1 to run acceptance tests")
	}
	name := acctestName("tf-acc-cluster")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: accClusterConfig(name),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("phasetwo_cluster.test", "name", name),
					resource.TestCheckResourceAttr("phasetwo_cluster.test", "status", "ACTIVE"),
					resource.TestCheckResourceAttr("phasetwo_cluster.test", "tier", "premium"),
					// billing_period is not readable back from the API, so it must come from
					// the schema default rather than being dropped.
					resource.TestCheckResourceAttr("phasetwo_cluster.test", "billing_period", "monthly"),
					resource.TestCheckResourceAttrSet("phasetwo_cluster.test", "id"),
					resource.TestCheckResourceAttrSet("phasetwo_cluster.test", "host"),
					resource.TestCheckResourceAttrSet("phasetwo_cluster.test", "created_at"),
				),
			},
			{
				ResourceName:      "phasetwo_cluster.test",
				ImportState:       true,
				ImportStateVerify: true,
				// Neither is returned by the API, so an import cannot recover them.
				ImportStateVerifyIgnore: []string{"payment_method_id", "billing_period"},
			},
		},
	})
}

func TestAccRealm(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("set TF_ACC=1 to run acceptance tests")
	}
	clusterName := acctestName("tf-acc-realm")

	config := func(displayName string) string {
		return accClusterConfig(clusterName) + fmt.Sprintf(`
resource "phasetwo_realm" "test" {
  cluster_id   = phasetwo_cluster.test.id
  name         = "accrealm"
  display_name = %[1]q
}
`, displayName)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("First Name"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("phasetwo_realm.test", "name", "accrealm"),
					resource.TestCheckResourceAttr("phasetwo_realm.test", "display_name", "First Name"),
					resource.TestCheckResourceAttr("phasetwo_realm.test", "state", "ACTIVE"),
				),
			},
			{
				// display_name is the only in-place update, and it goes through an endpoint that
				// validates the whole deployment. Changing it is the regression test for that.
				Config: config("Second Name"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("phasetwo_realm.test", "display_name", "Second Name"),
				),
			},
			{
				ResourceName:      "phasetwo_realm.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// A realm name with capitals is lowercased by the API, so the resource must settle on the
// lowered form rather than producing a permanently inconsistent plan.
func TestAccRealmLowercasesName(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("set TF_ACC=1 to run acceptance tests")
	}
	clusterName := acctestName("tf-acc-realmcase")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: accClusterConfig(clusterName) + `
resource "phasetwo_realm" "test" {
  cluster_id = phasetwo_cluster.test.id
  name       = "MixedCase"
}
`,
				Check: resource.TestCheckResourceAttr("phasetwo_realm.test", "name", "mixedcase"),
				// The API lowercases the name, so the applied state cannot match the config.
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func TestAccEnvironmentVariables(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("set TF_ACC=1 to run acceptance tests")
	}
	clusterName := acctestName("tf-acc-envvar")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Several at once on one cluster: each restarts Keycloak, and the API rejects a
				// second restart while one is in flight. This is the test for the restart gate.
				Config: accClusterConfig(clusterName) + `
resource "phasetwo_cluster_environment_variable" "one" {
  cluster_id = phasetwo_cluster.test.id
  name       = "KC_SPI_ACC_TEST_ONE"
  value      = "first"
}

resource "phasetwo_cluster_environment_variable" "two" {
  cluster_id = phasetwo_cluster.test.id
  name       = "KC_SPI_ACC_TEST_TWO"
  value      = "second"
}

resource "phasetwo_cluster_environment_variable" "secret" {
  cluster_id = phasetwo_cluster.test.id
  name       = "ACC_TEST_SECRET"
  value      = "hunter2"
  type       = "SECRET"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("phasetwo_cluster_environment_variable.one", "value", "first"),
					resource.TestCheckResourceAttr("phasetwo_cluster_environment_variable.two", "value", "second"),
					// A SECRET reads back masked; state must keep what was applied, not the mask.
					resource.TestCheckResourceAttr("phasetwo_cluster_environment_variable.secret", "value", "hunter2"),
				),
			},
		},
	})
}

func TestAccIPRules(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("set TF_ACC=1 to run acceptance tests")
	}
	clusterName := acctestName("tf-acc-iprules")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: accClusterConfig(clusterName) + `
resource "phasetwo_cluster_ip_rules" "test" {
  cluster_id = phasetwo_cluster.test.id

  realm_block {
    alias   = "blocked"
    address = "192.0.2.0/24"
  }
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("phasetwo_cluster_ip_rules.test", "realm_block.#", "1"),
					resource.TestCheckResourceAttr("phasetwo_cluster_ip_rules.test", "realm_block.0.address", "192.0.2.0/24"),
					resource.TestCheckResourceAttr("phasetwo_cluster_ip_rules.test", "admin_allow.#", "0"),
				),
			},
			{
				// Removing a category must actually clear it, since each write replaces the
				// whole list rather than merging.
				Config: accClusterConfig(clusterName) + `
resource "phasetwo_cluster_ip_rules" "test" {
  cluster_id = phasetwo_cluster.test.id
}
`,
				Check: resource.TestCheckResourceAttr("phasetwo_cluster_ip_rules.test", "realm_block.#", "0"),
			},
		},
	})
}

func TestAccDataSources(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("set TF_ACC=1 to run acceptance tests")
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
data "phasetwo_organizations" "all" {}

data "phasetwo_regions" "all" {}

data "phasetwo_payment_method" "default" {
  organization_id = %[1]q
  default         = true
}
`, os.Getenv("PHASETWO_TEST_ORG_ID")),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.phasetwo_organizations.all", "organizations.#"),
					resource.TestCheckResourceAttrSet("data.phasetwo_regions.all", "names.#"),
					resource.TestCheckResourceAttrSet("data.phasetwo_payment_method.default", "id"),
				),
			},
		},
	})
}
