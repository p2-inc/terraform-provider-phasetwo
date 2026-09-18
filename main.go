// terraform-provider-phasetwo manages Phase Two hosted Keycloak clusters and realms.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/p2-inc/terraform-provider-phasetwo/internal/provider"
)

// version is set by GoReleaser at build time via -ldflags. "dev" is what a local `go build`
// produces, and is also what the provider reports in its User-Agent.
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false,
		"run the provider in debug mode, for attaching a debugger")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/p2-inc/terraform-provider-phasetwo",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
