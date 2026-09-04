package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/p2-inc/terraform-provider-phasetwo/internal/client"
)

// Cluster states the provider reasons about. The spec now types these, so these are the
// generated constants rather than strings.
const (
	ClusterActive          = client.ClusterState("ACTIVE")
	ClusterProvisioning    = client.ClusterState("PROVISIONING")
	ClusterBillingSetup    = client.ClusterState("BILLING_SETUP")
	ClusterPendingPayment  = client.ClusterState("PENDING_PAYMENT")
	ClusterSetupException  = client.ClusterState("SETUP_EXCEPTION")
	ClusterBillingRequired = client.ClusterState("BILLING_REQUIRED")
	ClusterPendingDeletion = client.ClusterState("PENDING_DELETION")
	ClusterArchived        = client.ClusterState("ARCHIVED")
	ClusterDisabled        = client.ClusterState("DISABLED")
)

// ErrCheckoutRequired is returned when cluster creation came back with a Stripe Checkout link
// instead of a cluster. That is a browser flow and cannot be completed from Terraform.
var ErrCheckoutRequired = errors.New("cluster creation requires completing Stripe Checkout in a browser")

// ErrPaymentActionRequired is returned when the payment method needs 3DS/SCA confirmation. The
// cluster already exists in this case — CreatedClusterID carries its id so the caller can record
// it rather than leak it.
type ErrPaymentActionRequired struct {
	CreatedClusterID string
}

func (e *ErrPaymentActionRequired) Error() string {
	return fmt.Sprintf("cluster %s was created but its payment method requires additional "+
		"authentication (3DS/SCA), which must be completed in a browser", e.CreatedClusterID)
}

// ListClusters returns every non-archived cluster the caller can see.
func (c *Client) ListClusters(ctx context.Context) ([]client.Cluster, error) {
	const op = "cluster.list"
	resp, err := c.api.ClusterListWithResponse(ctx)
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

// GetCluster returns one cluster by id.
func (c *Client) GetCluster(ctx context.Context, id string) (*client.Cluster, error) {
	const op = "cluster.detail"
	resp, err := c.api.ClusterDetailWithResponse(ctx, id)
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

// CreateCluster creates a dedicated cluster and returns it.
//
// The API's response is a three-way union expressed as five optional fields, and only one of the
// three shapes is usable without a browser. Both other shapes become typed errors so the caller
// can report something actionable — and, in the 3DS case, still record the cluster that now
// exists.
func (c *Client) CreateCluster(ctx context.Context, req client.DedicatedClusterRequest) (*client.Cluster, error) {
	const op = "cluster.create"
	resp, err := c.api.ClusterCreateWithResponse(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	if err := check(op, resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, missingBody(op, resp.StatusCode())
	}
	res := resp.JSON200

	switch {
	case res.Cluster != nil:
		return res.Cluster, nil
	case res.RequiresAction:
		id := ""
		if res.ClusterId != nil {
			id = *res.ClusterId
		}
		return nil, &ErrPaymentActionRequired{CreatedClusterID: id}
	case res.Link != nil:
		return nil, ErrCheckoutRequired
	default:
		return nil, fmt.Errorf("%s: response matched none of the documented shapes "+
			"(no cluster, no link, requires_action false)", op)
	}
}

// DeleteCluster schedules a cluster for removal.
//
// This is not immediate: unless the cluster never completed billing setup, it moves to
// PENDING_DELETION and is torn down at the end of the billing cycle. The caller is expected to
// tell the user that.
func (c *Client) DeleteCluster(ctx context.Context, id string) error {
	const op = "cluster.delete"
	resp, err := c.api.ClusterDeleteWithResponse(ctx, id)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	return check(op, resp.StatusCode(), resp.Body)
}

// SetClusterHost switches the cluster's primary hostname to an already-provisioned domain.
func (c *Client) SetClusterHost(ctx context.Context, id, host string) (*client.Cluster, error) {
	const op = "cluster.host.update"
	resp, err := c.api.ClusterHostUpdateWithResponse(ctx, id, client.ClusterDomain{Host: host})
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

// ListRegions returns the regions a new cluster can be provisioned into.
func (c *Client) ListRegions(ctx context.Context) ([]client.Region, error) {
	const op = "cluster.region.list"
	resp, err := c.api.ClusterRegionListWithResponse(ctx)
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

// ClusterNameAvailable reports whether a candidate cluster name can be used.
func (c *Client) ClusterNameAvailable(ctx context.Context, name string) (bool, string, error) {
	const op = "cluster.nameAvailability.check"
	resp, err := c.api.ClusterNameAvailabilityCheckWithResponse(ctx,
		&client.ClusterNameAvailabilityCheckParams{Name: name})
	if err != nil {
		return false, "", fmt.Errorf("%s: %w", op, err)
	}
	if err := check(op, resp.StatusCode(), resp.Body); err != nil {
		return false, "", err
	}
	if resp.JSON200 == nil {
		return false, "", missingBody(op, resp.StatusCode())
	}
	rec := ""
	if resp.JSON200.Recommendation != nil {
		rec = *resp.JSON200.Recommendation
	}
	return resp.JSON200.Available, rec, nil
}

// ClusterTerminal reports whether a state is one the cluster will not leave on its own.
func ClusterTerminal(s client.ClusterState) bool {
	switch s {
	case ClusterActive, ClusterArchived, ClusterPendingDeletion,
		ClusterSetupException, ClusterDisabled, ClusterBillingRequired:
		return true
	}
	return false
}

// ClusterGone reports whether a state means the cluster should be treated as absent by Terraform.
// PENDING_DELETION is included deliberately: the cluster still answers reads, but it is scheduled
// for teardown and its name is already spent.
func ClusterGone(s client.ClusterState) bool {
	return s == ClusterArchived || s == ClusterPendingDeletion
}

// WaitForClusterActive polls until the cluster reaches ACTIVE, or fails.
func (c *Client) WaitForClusterActive(ctx context.Context, id string, timeout time.Duration) (*client.Cluster, error) {
	const interval = 15 * time.Second
	deadline := time.Now().Add(timeout)

	for {
		cl, err := c.GetCluster(ctx, id)
		if err != nil {
			return nil, err
		}
		switch {
		case cl.Status == ClusterActive:
			return cl, nil
		case cl.Status == ClusterSetupException:
			return cl, fmt.Errorf("cluster %s failed to provision (state %s)", id, cl.Status)
		case ClusterGone(cl.Status):
			return cl, fmt.Errorf("cluster %s went to %s while waiting for it to become active",
				id, cl.Status)
		case cl.Status == ClusterBillingSetup || cl.Status == ClusterPendingPayment:
			// Provisioning has not started because payment has not settled. Nothing Terraform
			// can do about that, and waiting out the full timeout would hide the reason.
			return cl, fmt.Errorf("cluster %s is in %s: provisioning does not begin until payment "+
				"settles, which cannot be completed from Terraform", id, cl.Status)
		}

		if time.Now().After(deadline) {
			return cl, fmt.Errorf("timed out after %s waiting for cluster %s to become active "+
				"(last state %s)", timeout, id, cl.Status)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// WaitForClusterGone polls after a delete until the cluster reads as absent or scheduled for
// deletion. It returns the last observed state so the caller can distinguish "really gone" from
// "scheduled", which is the difference between a clean destroy and one the user should know is
// still billing.
func (c *Client) WaitForClusterGone(ctx context.Context, id string, timeout time.Duration) (client.ClusterState, error) {
	const interval = 10 * time.Second
	deadline := time.Now().Add(timeout)

	var last client.ClusterState
	for {
		cl, err := c.GetCluster(ctx, id)
		if err != nil {
			if IsNotFound(err) {
				return last, nil
			}
			return last, err
		}
		last = cl.Status
		if ClusterGone(cl.Status) {
			return last, nil
		}
		if time.Now().After(deadline) {
			return last, fmt.Errorf("timed out after %s waiting for cluster %s to be removed "+
				"(last state %s)", timeout, id, last)
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(interval):
		}
	}
}
