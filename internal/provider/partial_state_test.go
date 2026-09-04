package provider

import (
	"context"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

// Several resources save a partial result mid-Create so that a long wait, or an operator hitting
// Ctrl-C during one, cannot lose the id of something that already exists and bills. Terraform
// state cannot hold unknown values, so everything in that partial save has to be known or null.
//
// This walks each model's computed-only attributes and checks the model has been through its
// markComputedUnset (or equivalent) before the early save. It fails if someone adds a computed
// attribute and forgets to null it — which would otherwise only show up as a confusing
// "unexpected new value" from Terraform in the middle of a real provisioning run.
func TestPartialStateHasNoUnknowns(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name  string
		res   resource.Resource
		unset func() any // a model as it is saved mid-Create
	}{
		{
			name: "phasetwo_cluster",
			res:  NewClusterResource(),
			unset: func() any {
				var m clusterModel
				m.markComputedUnset()
				return m
			},
		},
		{
			name: "phasetwo_realm",
			res:  NewRealmResource(),
			unset: func() any {
				var m realmModel
				m.markComputedUnset()
				return m
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schemaResp := &resource.SchemaResponse{}
			tc.res.Schema(ctx, resource.SchemaRequest{}, schemaResp)
			if schemaResp.Diagnostics.HasError() {
				t.Fatalf("schema: %v", schemaResp.Diagnostics)
			}

			computedOnly := map[string]bool{}
			for name, a := range schemaResp.Schema.Attributes {
				if a.IsComputed() && !a.IsOptional() && !a.IsRequired() {
					computedOnly[name] = true
				}
			}
			// `id` is set explicitly alongside the partial save, so it is expected to be known.
			delete(computedOnly, "id")

			model := tc.unset()
			v := reflect.ValueOf(model)
			ty := v.Type()

			seen := map[string]bool{}
			for i := 0; i < ty.NumField(); i++ {
				tag := ty.Field(i).Tag.Get("tfsdk")
				if !computedOnly[tag] {
					continue
				}
				seen[tag] = true

				av, ok := v.Field(i).Interface().(attr.Value)
				if !ok {
					t.Errorf("field %s (%s) is not an attr.Value", ty.Field(i).Name, tag)
					continue
				}
				if av.IsUnknown() {
					t.Errorf("%s is still unknown after markComputedUnset; Terraform state "+
						"cannot hold unknown values, so the partial save would be rejected", tag)
				}
			}

			for name := range computedOnly {
				if !seen[name] {
					t.Errorf("computed attribute %s has no matching model field, so "+
						"markComputedUnset cannot be covering it", name)
				}
			}
		})
	}
}
