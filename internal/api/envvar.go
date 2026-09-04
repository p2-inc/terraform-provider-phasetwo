package api

import (
	"context"
	"fmt"
	"regexp"

	"github.com/p2-inc/terraform-provider-phasetwo/internal/client"
)

// Environment variable storage kinds.
const (
	EnvString = client.EnvironmentVariableType("STRING")
	EnvSecret = client.EnvironmentVariableType("SECRET")
)

// SecretValueMask is what the API returns in place of a SECRET variable's value. Reads never
// expose the real value, so the provider must not treat this as drift.
const SecretValueMask = "************"

var (
	keycloakPrefix = regexp.MustCompile(`^KC_`)
	customSPI      = regexp.MustCompile(`^KC_SPI_`)
)

// ValidEnvVarName mirrors the server's CustomEnvironmentVariableValidator: a name is accepted
// when it targets custom SPI config (`KC_SPI_…`) or is not a Keycloak variable at all. Checking
// here turns a 400 at apply time into a plan-time error.
func ValidEnvVarName(name string) bool {
	if name == "" {
		return false
	}
	if customSPI.MatchString(name) {
		return true
	}
	return !keycloakPrefix.MatchString(name)
}

// ListEnvVars returns a cluster's custom environment variables. SECRET values come back masked.
func (c *Client) ListEnvVars(ctx context.Context, clusterID string) ([]client.EnvironmentVariableRepresentation, error) {
	const op = "cluster.envVar.list"
	resp, err := c.api.ClusterEnvVarListWithResponse(ctx, clusterID)
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

// GetEnvVar returns one environment variable. A SECRET value comes back masked.
func (c *Client) GetEnvVar(ctx context.Context, clusterID, id string) (*client.EnvironmentVariableRepresentation, error) {
	const op = "cluster.envVar.detail"
	resp, err := c.api.ClusterEnvVarDetailWithResponse(ctx, clusterID, id)
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

// CreateEnvVar adds an environment variable and restarts the cluster to apply it.
func (c *Client) CreateEnvVar(ctx context.Context, clusterID string, req client.EnvironmentVariableRequest) (*client.EnvironmentVariableRepresentation, error) {
	const op = "cluster.envVar.create"
	var out *client.EnvironmentVariableRepresentation

	err := c.DoRestarting(ctx, clusterID, func() error {
		resp, err := c.api.ClusterEnvVarCreateWithResponse(ctx, clusterID, req)
		if err != nil {
			return fmt.Errorf("%s: %w", op, err)
		}
		if err := check(op, resp.StatusCode(), resp.Body); err != nil {
			return err
		}
		if resp.JSON200 == nil {
			return missingBody(op, resp.StatusCode())
		}
		out = resp.JSON200
		return nil
	})
	return out, err
}

// UpdateEnvVar changes an environment variable and restarts the cluster to apply it.
func (c *Client) UpdateEnvVar(ctx context.Context, clusterID, id string, req client.EnvironmentVariableRequest) (*client.EnvironmentVariableRepresentation, error) {
	const op = "cluster.envVar.update"
	var out *client.EnvironmentVariableRepresentation

	err := c.DoRestarting(ctx, clusterID, func() error {
		resp, err := c.api.ClusterEnvVarUpdateWithResponse(ctx, clusterID, id, req)
		if err != nil {
			return fmt.Errorf("%s: %w", op, err)
		}
		if err := check(op, resp.StatusCode(), resp.Body); err != nil {
			return err
		}
		if resp.JSON200 == nil {
			return missingBody(op, resp.StatusCode())
		}
		out = resp.JSON200
		return nil
	})
	return out, err
}

// DeleteEnvVar removes an environment variable and restarts the cluster to apply it.
func (c *Client) DeleteEnvVar(ctx context.Context, clusterID, id string) error {
	const op = "cluster.envVar.delete"
	return c.DoRestarting(ctx, clusterID, func() error {
		resp, err := c.api.ClusterEnvVarDeleteWithResponse(ctx, clusterID, id)
		if err != nil {
			return fmt.Errorf("%s: %w", op, err)
		}
		return check(op, resp.StatusCode(), resp.Body)
	})
}
