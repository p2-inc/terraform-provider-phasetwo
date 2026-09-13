package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/p2-inc/terraform-provider-phasetwo/internal/client"
)

// ErrCredentialNotFound reports a credential that is no longer on the realm, so callers can tell
// "somebody revoked it" apart from "the API is unreachable". Terraform needs that distinction: the
// first means remove it from state and recreate, the second means fail the run.
var ErrCredentialNotFound = fmt.Errorf("credential not found")

// CreateCredential provisions a service account client on a realm and returns it, including the
// secret from the create response.
func (c *Client) CreateCredential(
	ctx context.Context, realmID, name, description string, roles []string,
) (*client.DeploymentCredential, error) {
	const op = "deployment.credential.create"

	body := client.DeploymentCredentialCreateJSONRequestBody{}
	if name != "" {
		body.Name = &name
	}
	if description != "" {
		body.Description = &description
	}
	if len(roles) > 0 {
		body.Roles = &roles
	}

	resp, err := c.api.DeploymentCredentialCreateWithResponse(ctx, realmID, body)
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

// GetCredential returns one credential by client id, without its secret.
//
// The API has no single-credential read that omits the secret, so this filters the list. That is
// deliberate rather than lazy: a Terraform Read runs on every plan, and reading the secret on
// every plan would pull a realm-admin credential through the provider for no reason — the resource
// does not keep it.
//
// Returns ErrCredentialNotFound when it is gone.
func (c *Client) GetCredential(
	ctx context.Context, realmID, clientID string,
) (*client.DeploymentCredential, error) {
	credentials, err := c.ListCredentials(ctx, realmID)
	if err != nil {
		return nil, err
	}
	for i := range credentials {
		if credentials[i].ClientId == clientID {
			return &credentials[i], nil
		}
	}
	return nil, fmt.Errorf("%w: %s on realm %s", ErrCredentialNotFound, clientID, realmID)
}

// ListCredentials returns the credentials on a realm, with their roles but without their secrets.
func (c *Client) ListCredentials(ctx context.Context, realmID string) ([]client.DeploymentCredential, error) {
	const op = "deployment.credential.list"
	resp, err := c.api.DeploymentCredentialListWithResponse(ctx, realmID)
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

// ReadCredentialSecret fetches a credential's secret from the realm.
//
// Phase Two stores no copy; this reads it from the customer's Keycloak, where it has always lived.
// Reading does not rotate it, so calling this on every apply is safe and is the point: a secret
// fetched when needed does not have to be written to Terraform state.
//
// Returns ErrCredentialNotFound when the credential has been revoked.
func (c *Client) ReadCredentialSecret(
	ctx context.Context, realmID, clientID string,
) (*client.DeploymentCredential, error) {
	const op = "deployment.credential.secret.read"
	resp, err := c.api.DeploymentCredentialSecretReadWithResponse(ctx, realmID, clientID)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	if resp.StatusCode() == http.StatusNotFound {
		return nil, fmt.Errorf("%w: %s on realm %s", ErrCredentialNotFound, clientID, realmID)
	}
	if err := check(op, resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, missingBody(op, resp.StatusCode())
	}
	return resp.JSON200, nil
}

// DeleteCredential revokes a credential, deleting the client from the realm.
//
// A credential that is already gone is not an error: Terraform destroying something somebody
// already revoked by hand should succeed, not wedge the state.
func (c *Client) DeleteCredential(ctx context.Context, realmID, clientID string) error {
	const op = "deployment.credential.delete"
	resp, err := c.api.DeploymentCredentialDeleteWithResponse(ctx, realmID, clientID)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	if resp.StatusCode() == http.StatusNotFound {
		return nil
	}
	return check(op, resp.StatusCode(), resp.Body)
}
