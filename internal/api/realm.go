package api

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/p2-inc/terraform-provider-phasetwo/internal/client"
)

// Deployment (realm) states.
const (
	RealmPending  = client.DeploymentState("PENDING")
	RealmActive   = client.DeploymentState("ACTIVE")
	RealmDisabled = client.DeploymentState("DISABLED")
	RealmFailed   = client.DeploymentState("FAILED")
)

// CreateRealm creates a deployment on a cluster and returns its id.
//
// The API answers 201 with no body, so the id comes from the Location header — the last path
// segment of the created deployment's URL. That header is documented in the spec, so relying on
// it is not an implementation detail. If it is somehow absent, fall back to matching by name.
func (c *Client) CreateRealm(ctx context.Context, clusterID, name string) (string, error) {
	const op = "cluster.deployment.create"
	resp, err := c.api.ClusterDeploymentCreateWithResponse(ctx, clusterID,
		client.ClusterDeploymentRequest{Name: name})
	if err != nil {
		return "", fmt.Errorf("%s: %w", op, err)
	}
	if err := check(op, resp.StatusCode(), resp.Body); err != nil {
		return "", err
	}

	if resp.Headers201 != nil && resp.Headers201.Location != nil {
		if id := idFromLocation(*resp.Headers201.Location); id != "" {
			return id, nil
		}
	}

	// The API lowercases names on create, so match on the lowered form.
	lowered := strings.ToLower(name)
	realms, listErr := c.ListRealms(ctx, clusterID, &lowered)
	if listErr != nil {
		return "", fmt.Errorf("%s: created the realm but could not determine its id "+
			"(no usable Location header, and listing failed): %w", op, listErr)
	}
	for _, r := range realms {
		if r.Name == lowered {
			return r.Id, nil
		}
	}
	return "", fmt.Errorf("%s: created the realm but could not determine its id: "+
		"no Location header and no realm named %q on cluster %s", op, lowered, clusterID)
}

// idFromLocation extracts the trailing path segment of a Location header.
func idFromLocation(loc string) string {
	u, err := url.Parse(loc)
	if err != nil {
		return ""
	}
	p := strings.TrimRight(u.Path, "/")
	if p == "" {
		return ""
	}
	return path.Base(p)
}

// GetRealm returns one deployment by id.
func (c *Client) GetRealm(ctx context.Context, id string) (*client.Deployment, error) {
	const op = "deployment.detail"
	resp, err := c.api.DeploymentDetailWithResponse(ctx, id)
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

// ListRealms returns the deployments on a cluster, optionally filtered by name substring.
func (c *Client) ListRealms(ctx context.Context, clusterID string, search *string) ([]client.Deployment, error) {
	const op = "cluster.deployment.list"
	params := &client.ClusterDeploymentListParams{Search: search}
	resp, err := c.api.ClusterDeploymentListWithResponse(ctx, clusterID, params)
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

// UpdateRealm changes a deployment's mutable settings.
//
// Only display_name has any effect — Converters.mergeDeployment ignores every other field — but
// the endpoint still runs Deployments.validateDeployment on the request first, which rejects a
// blank name or a null organizationId. So the current name and org must be echoed back even
// though they are read-only in practice; sending display_name alone is a guaranteed 400.
//
// That is why this reads the realm before writing it.
func (c *Client) UpdateRealm(ctx context.Context, id, displayName string) error {
	const op = "deployment.update"

	current, err := c.GetRealm(ctx, id)
	if err != nil {
		return err
	}
	if current.OrgId == nil {
		return fmt.Errorf("%s: realm %s has no organization, which the update endpoint requires",
			op, id)
	}

	body := client.Deployment{
		Id:          current.Id,
		Name:        current.Name,
		State:       current.State,
		OrgId:       current.OrgId,
		DisplayName: &displayName,
	}
	resp, err := c.api.DeploymentUpdateWithResponse(ctx, id, body)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	return check(op, resp.StatusCode(), resp.Body)
}

// DeleteRealm permanently removes a deployment.
func (c *Client) DeleteRealm(ctx context.Context, id string) error {
	const op = "deployment.delete"
	resp, err := c.api.DeploymentDeleteWithResponse(ctx, id)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	return check(op, resp.StatusCode(), resp.Body)
}

// WaitForRealmActive polls until the deployment leaves PENDING.
func (c *Client) WaitForRealmActive(ctx context.Context, id string, timeout time.Duration) (*client.Deployment, error) {
	const interval = 10 * time.Second
	deadline := time.Now().Add(timeout)

	for {
		d, err := c.GetRealm(ctx, id)
		if err != nil {
			return nil, err
		}
		switch d.State {
		case RealmActive:
			return d, nil
		case RealmFailed:
			msg := "no reason given"
			if d.StateError != nil && *d.StateError != "" {
				msg = *d.StateError
			}
			return d, fmt.Errorf("realm %s failed to provision: %s", id, msg)
		}

		if time.Now().After(deadline) {
			return d, fmt.Errorf("timed out after %s waiting for realm %s to become active "+
				"(last state %s)", timeout, id, d.State)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}
