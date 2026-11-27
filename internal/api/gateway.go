package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/p2-inc/terraform-provider-phasetwo/internal/client"
)

// Gateway app statuses, as the API reports them.
const (
	GatewayAppPending  = client.ProtectedAppStatus("PENDING")
	GatewayAppActive   = client.ProtectedAppStatus("ACTIVE")
	GatewayAppDisabled = client.ProtectedAppStatus("DISABLED")

	GatewayVerified = client.ProtectedAppVerificationResult("VERIFIED")
)

// CreateGatewayApp creates a Gateway app on a realm. The response carries the origin secret, which
// the API never returns again.
func (c *Client) CreateGatewayApp(
	ctx context.Context, realmID string, body client.ProtectedAppRequest,
) (*client.ProtectedAppSecret, error) {
	const op = "deployment.protectedApp.create"
	resp, err := c.api.DeploymentProtectedAppCreateWithResponse(ctx, realmID, body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	if err := check(op, resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON201 == nil {
		return nil, missingBody(op, resp.StatusCode())
	}
	return resp.JSON201, nil
}

// GetGatewayApp reads one app, including its live hostname readiness.
func (c *Client) GetGatewayApp(ctx context.Context, realmID, appID string) (*client.ProtectedApp, error) {
	const op = "deployment.protectedApp.detail"
	resp, err := c.api.DeploymentProtectedAppDetailWithResponse(ctx, realmID, appID)
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

// UpdateGatewayApp replaces the app's settings. Fields left nil keep their current value, so the
// provider sends every field it manages.
func (c *Client) UpdateGatewayApp(
	ctx context.Context, realmID, appID string, body client.ProtectedAppRequest,
) (*client.ProtectedApp, error) {
	const op = "deployment.protectedApp.update"
	resp, err := c.api.DeploymentProtectedAppUpdateWithResponse(ctx, realmID, appID, body)
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

// DeleteGatewayApp stops serving the app and removes it. An app that is already gone is not an
// error.
func (c *Client) DeleteGatewayApp(ctx context.Context, realmID, appID string) error {
	const op = "deployment.protectedApp.delete"
	resp, err := c.api.DeploymentProtectedAppDeleteWithResponse(ctx, realmID, appID)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	if resp.StatusCode() == http.StatusNotFound {
		return nil
	}
	return check(op, resp.StatusCode(), resp.Body)
}

// VerifyGatewayApp probes the origin with and without the current secret and records the result.
func (c *Client) VerifyGatewayApp(ctx context.Context, realmID, appID string) (*client.ProtectedApp, error) {
	const op = "deployment.protectedApp.verify"
	resp, err := c.api.DeploymentProtectedAppVerifyWithResponse(ctx, realmID, appID, nil)
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

// ActivateGatewayApp starts serving the app. It is a 409 until the origin is verified and the
// hostname and certificate are active.
func (c *Client) ActivateGatewayApp(ctx context.Context, realmID, appID string) (*client.ProtectedApp, error) {
	const op = "deployment.protectedApp.activate"
	resp, err := c.api.DeploymentProtectedAppActivateWithResponse(ctx, realmID, appID)
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

// DeactivateGatewayApp stops serving the app and ends its sessions.
func (c *Client) DeactivateGatewayApp(ctx context.Context, realmID, appID string) error {
	const op = "deployment.protectedApp.deactivate"
	resp, err := c.api.DeploymentProtectedAppDeactivateWithResponse(ctx, realmID, appID)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	return check(op, resp.StatusCode(), resp.Body)
}
