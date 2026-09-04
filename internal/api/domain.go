package api

import (
	"context"
	"fmt"

	"github.com/p2-inc/terraform-provider-phasetwo/internal/client"
)

// Certificate states worth branching on.
const (
	CertIssued            = client.CertificateStatusMapping("ISSUED")
	CertPendingValidation = client.CertificateStatusMapping("PENDING_VALIDATION")
	CertFailed            = client.CertificateStatusMapping("FAILED")
	CertTimedOut          = client.CertificateStatusMapping("VALIDATION_TIMED_OUT")
	CertRevoked           = client.CertificateStatusMapping("REVOKED")
	CertExpired           = client.CertificateStatusMapping("EXPIRED")
	CertInactive          = client.CertificateStatusMapping("INACTIVE")
	CertNotFound          = client.CertificateStatusMapping("NOT_FOUND")
)

// CreateDomain registers a custom hostname and starts DNS/TLS provisioning.
//
// The response is a CustomerDomainValidation, not a bare domain — it carries the DNS records to
// create along with the current validation and certificate status, so a caller gets everything
// it needs from this one call.
func (c *Client) CreateDomain(ctx context.Context, clusterID, host string) (*client.CustomerDomainValidation, error) {
	const op = "cluster.domain.create"
	resp, err := c.api.ClusterDomainCreateWithResponse(ctx, clusterID, client.ClusterDomain{Host: host})
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

// GetDomain returns one custom domain by id.
func (c *Client) GetDomain(ctx context.Context, clusterID, domainID string) (*client.CustomerDomain, error) {
	const op = "cluster.domain.detail"
	resp, err := c.api.ClusterDomainDetailWithResponse(ctx, clusterID, domainID)
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

// GetDomainStatus returns a domain's DNS validation and certificate status.
func (c *Client) GetDomainStatus(ctx context.Context, clusterID, domainID string) (*client.CustomerDomainValidation, error) {
	const op = "cluster.domain.status.detail"
	resp, err := c.api.ClusterDomainStatusDetailWithResponse(ctx, clusterID, domainID)
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

// ListDomains returns the custom domains on a cluster.
func (c *Client) ListDomains(ctx context.Context, clusterID string) ([]client.CustomerDomain, error) {
	const op = "cluster.domain.list"
	resp, err := c.api.ClusterDomainListWithResponse(ctx, clusterID)
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

// DeleteDomain removes a custom domain. It fails if the domain is the cluster's primary host.
func (c *Client) DeleteDomain(ctx context.Context, clusterID, domainID string) error {
	const op = "cluster.domain.delete"
	resp, err := c.api.ClusterDomainDeleteWithResponse(ctx, clusterID, domainID)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	return check(op, resp.StatusCode(), resp.Body)
}
