package api

import (
	"context"
	"fmt"

	"github.com/p2-inc/terraform-provider-phasetwo/internal/client"
)

// ListOrgs returns the organizations the authenticated client belongs to.
func (c *Client) ListOrgs(ctx context.Context) ([]client.Org, error) {
	const op = "org.list"
	resp, err := c.api.OrgListWithResponse(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	if err := check(op, resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, missingBody(op, resp.StatusCode())
	}
	return *resp.JSON200, nil
}

// ListPaymentMethods returns the saved cards on an organization's Stripe customer. It is empty
// when the organization has no Stripe customer yet.
func (c *Client) ListPaymentMethods(ctx context.Context, orgID string) ([]client.PaymentMethod, error) {
	const op = "org.paymentMethod.list"
	resp, err := c.api.OrgPaymentMethodListWithResponse(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	if err := check(op, resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, missingBody(op, resp.StatusCode())
	}
	return *resp.JSON200, nil
}
