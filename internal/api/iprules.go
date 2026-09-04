package api

import (
	"context"
	"fmt"

	"github.com/p2-inc/terraform-provider-phasetwo/internal/client"
)

// GetIPRules returns a cluster's IP rules, grouped by category.
func (c *Client) GetIPRules(ctx context.Context, clusterID string) (*client.IpRulesRepresentation, error) {
	const op = "cluster.ipRule.list"
	resp, err := c.api.ClusterIpRuleListWithResponse(ctx, clusterID)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	if err := check(op, resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, missingBody(op, resp.StatusCode())
	}
	return resp.JSON200, nil
}

// SetIPRules replaces a cluster's IP rules.
//
// Each category included replaces that category wholesale; a category left nil is untouched.
// This provider always sends all three, because the resource models the complete set — leaving
// one out would make "I removed the block list from my config" a no-op.
func (c *Client) SetIPRules(ctx context.Context, clusterID string, req client.IpRestrictionsRequest) (*client.IpRulesRepresentation, error) {
	const op = "cluster.ipRule.update"
	resp, err := c.api.ClusterIpRuleUpdateWithResponse(ctx, clusterID, req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	if err := check(op, resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, missingBody(op, resp.StatusCode())
	}
	return resp.JSON200, nil
}
