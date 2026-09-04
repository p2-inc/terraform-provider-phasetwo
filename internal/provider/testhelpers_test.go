package provider

import (
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// emptyProviderConfig builds a ConfigureProvider payload with every provider attribute null,
// which is what Terraform sends for `provider "phasetwo" {}`. Encoding it by hand is the only
// way to drive ConfigureProvider without standing up a full acceptance test.
func emptyProviderConfig(schema *tfprotov6.GetProviderSchemaResponse) (*tfprotov6.DynamicValue, error) {
	if schema.Provider == nil || schema.Provider.Block == nil {
		return nil, fmt.Errorf("provider schema has no block")
	}

	attrTypes := make(map[string]tftypes.Type, len(schema.Provider.Block.Attributes))
	values := make(map[string]tftypes.Value, len(schema.Provider.Block.Attributes))
	for _, attr := range schema.Provider.Block.Attributes {
		attrTypes[attr.Name] = attr.Type
		values[attr.Name] = tftypes.NewValue(attr.Type, nil)
	}

	objType := tftypes.Object{AttributeTypes: attrTypes}
	dv, err := tfprotov6.NewDynamicValue(objType, tftypes.NewValue(objType, values))
	if err != nil {
		return nil, err
	}
	return &dv, nil
}

// acctestName builds a unique resource name. Cluster names stay reserved after a destroy
// (PENDING_DELETION holds them until the billing cycle ends), so reusing one across runs would
// fail with a 409.
func acctestName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano()%1_000_000_000)
}
