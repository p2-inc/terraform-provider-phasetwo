SHELL := /bin/bash
BINARY := terraform-provider-phasetwo
VERSION ?= dev

# Where `make sync-spec` looks for a phasetwo-keycloak checkout to regenerate the API spec from.
PHASETWO_KEYCLOAK ?= ../phasetwo-keycloak

.PHONY: default
default: build

.PHONY: build
build:
	go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY) .

.PHONY: install
install:
	go install -ldflags "-X main.version=$(VERSION)" .

.PHONY: test
test:
	go test ./... -timeout=120s

.PHONY: testacc
testacc: ## Acceptance tests. Needs real credentials; creates and destroys real infrastructure.
	TF_ACC=1 go test ./... -v -timeout=180m

.PHONY: fmt
fmt:
	gofmt -s -w .
	terraform fmt -recursive ./examples

.PHONY: vet
vet:
	go vet ./...

.PHONY: lint
lint:
	golangci-lint run

.PHONY: generate
generate: ## Regenerate the API client from the vendored spec.
	cd api && go tool oapi-codegen -config oapi-codegen.yaml openapi.yaml
	gofmt -w internal/client

.PHONY: docs
docs: ## Regenerate docs/ from schema descriptions, examples/ and templates/.
	go tool tfplugindocs generate --provider-name phasetwo

.PHONY: sync-spec
sync-spec: ## Rebuild the OpenAPI spec from a phasetwo-keycloak checkout and diff it in.
	@test -d "$(PHASETWO_KEYCLOAK)" || \
		{ echo "Set PHASETWO_KEYCLOAK to a phasetwo-keycloak checkout (currently $(PHASETWO_KEYCLOAK))"; exit 1; }
	cd "$(PHASETWO_KEYCLOAK)" && mvn -q -pl phasetwo-module -am -DskipTests process-classes
	@echo "--- diff against the vendored spec (api/openapi.yaml) ---"
	@diff -u api/openapi.yaml "$(PHASETWO_KEYCLOAK)/phasetwo-module/target/generated/openapi.yaml" || true
	@echo
	@echo "Copying. Remember to update the pinned commit in api/SPEC.md, then run 'make generate'."
	cp "$(PHASETWO_KEYCLOAK)/phasetwo-module/target/generated/openapi.yaml" api/openapi.yaml

.PHONY: help
help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-16s\033[0m %s\n", $$1, $$2}'
