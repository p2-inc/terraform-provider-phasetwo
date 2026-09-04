package api

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/p2-inc/terraform-provider-phasetwo/internal/client"
)

// Extension resource kinds.
const (
	ExtTheme             = client.ResourceType("THEME")
	ExtExtension         = client.ResourceType("EXTENSION")
	ExtPasswordBlacklist = client.ResourceType("PASSWORD_BLACKLIST")
	ExtWellKnown         = client.ResourceType("WELL_KNOWN")
)

// VersionDependent reports whether a resource type uses the versioned upload flow (a jar per
// Keycloak major version) rather than the standalone one (a single version-independent file).
// It mirrors ResourceType.isKeycloakVersionDependant server-side.
func VersionDependent(t client.ResourceType) bool {
	return t == ExtTheme || t == ExtExtension
}

// ListExtensions returns a cluster's extensions.
func (c *Client) ListExtensions(ctx context.Context, clusterID string) ([]client.Extension, error) {
	const op = "cluster.extension.list"
	resp, err := c.api.ClusterExtensionListWithResponse(ctx, clusterID, &client.ClusterExtensionListParams{})
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

// GetExtension returns one extension by id.
func (c *Client) GetExtension(ctx context.Context, clusterID, extensionID string) (*client.Extension, error) {
	const op = "cluster.extension.detail"
	resp, err := c.api.ClusterExtensionDetailWithResponse(ctx, clusterID, extensionID)
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

// CreateExtension registers an extension slot and returns its id, taken from the documented
// Location header on the 201.
func (c *Client) CreateExtension(ctx context.Context, clusterID, name string, rt client.ResourceType) (string, error) {
	const op = "cluster.extension.create"
	resp, err := c.api.ClusterExtensionCreateWithResponse(ctx, clusterID,
		client.ExtensionRequest{Name: name, ResourceType: rt})
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

	exts, listErr := c.ListExtensions(ctx, clusterID)
	if listErr != nil {
		return "", fmt.Errorf("%s: created the extension but could not determine its id "+
			"(no usable Location header, and listing failed): %w", op, listErr)
	}
	for _, e := range exts {
		if e.Name == name {
			return e.Id, nil
		}
	}
	return "", fmt.Errorf("%s: created the extension but could not determine its id: "+
		"no Location header and no extension named %q on cluster %s", op, name, clusterID)
}

// SetExtensionEnabled toggles an extension.
func (c *Client) SetExtensionEnabled(ctx context.Context, clusterID, extensionID string, enabled bool) (*client.Extension, error) {
	const op = "cluster.extension.update"
	resp, err := c.api.ClusterExtensionUpdateWithResponse(ctx, clusterID, extensionID,
		client.ToggleExtensionRequest{Enabled: enabled})
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

// DeleteExtension removes an extension and all of its versions.
func (c *Client) DeleteExtension(ctx context.Context, clusterID, extensionID string) error {
	const op = "cluster.extension.delete"
	resp, err := c.api.ClusterExtensionDeleteWithResponse(ctx, clusterID, extensionID)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	return check(op, resp.StatusCode(), resp.Body)
}

// ReconcileExtensions applies a cluster's configured extensions to its running deployment and
// restarts it. Extension changes have no effect until this runs.
func (c *Client) ReconcileExtensions(ctx context.Context, clusterID string) error {
	const op = "cluster.extension.reconcile"
	return c.DoRestarting(ctx, clusterID, func() error {
		resp, err := c.api.ClusterExtensionReconcileWithResponse(ctx, clusterID)
		if err != nil {
			return fmt.Errorf("%s: %w", op, err)
		}
		return check(op, resp.StatusCode(), resp.Body)
	})
}

// ListExtensionKeycloakVersions returns the Keycloak major versions a version-dependent
// extension may target.
func (c *Client) ListExtensionKeycloakVersions(ctx context.Context, clusterID string) ([]int32, error) {
	const op = "cluster.extension.keycloakVersion.list"
	resp, err := c.api.ClusterExtensionKeycloakVersionListWithResponse(ctx, clusterID)
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

// UploadVersion runs the whole upload flow for a version-dependent extension and returns the
// resulting version.
//
// Five calls plus an out-of-band S3 PUT:
//
//	create version → create upload URL → PUT the jar to S3 → confirm → (caller reconciles)
//
// The presigned response carries the HTTP method to use, so it is taken from there rather than
// assumed to be PUT.
func (c *Client) UploadVersion(
	ctx context.Context,
	clusterID, extensionID string,
	keycloakMajorVersion int32,
	label string,
	jar []byte,
) (*client.ExtensionVersion, error) {
	version, err := c.createVersion(ctx, clusterID, extensionID, keycloakMajorVersion)
	if err != nil {
		return nil, err
	}

	presigned, err := c.versionUploadURL(ctx, clusterID, extensionID, version.Id, label)
	if err != nil {
		return nil, err
	}
	if err := c.putObject(ctx, presigned, jar); err != nil {
		return nil, err
	}
	return c.confirmVersion(ctx, clusterID, extensionID, version.Id, presigned.ResourceKey)
}

// UploadStandalone runs the upload flow for a version-independent extension.
func (c *Client) UploadStandalone(
	ctx context.Context,
	clusterID, extensionID, label string,
	payload []byte,
) (*client.ExtensionVersion, error) {
	const op = "cluster.extension.standalone.uploadUrl.create"
	resp, err := c.api.ClusterExtensionStandaloneUploadUrlCreateWithResponse(ctx, clusterID, extensionID,
		client.ExtensionVersionUploadUrlRequest{Label: &label})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	if err := check(op, resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, missingBody(op, resp.StatusCode())
	}
	presigned := resp.JSON200

	if err := c.putObject(ctx, presigned, payload); err != nil {
		return nil, err
	}

	const confirmOp = "cluster.extension.standalone.confirm"
	cResp, err := c.api.ClusterExtensionStandaloneConfirmWithResponse(ctx, clusterID, extensionID,
		client.ExtensionVersionLocationValidationRequest{ResourceKey: presigned.ResourceKey})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", confirmOp, err)
	}
	if err := check(confirmOp, cResp.StatusCode(), cResp.Body); err != nil {
		return nil, err
	}
	if cResp.JSON200 == nil {
		return nil, missingBody(confirmOp, cResp.StatusCode())
	}
	return cResp.JSON200, nil
}

func (c *Client) createVersion(ctx context.Context, clusterID, extensionID string, kcMajor int32) (*client.ExtensionVersion, error) {
	const op = "cluster.extension.version.create"
	resp, err := c.api.ClusterExtensionVersionCreateWithResponse(ctx, clusterID, extensionID,
		client.ExtensionVersionRequest{KeycloakMajorVersion: kcMajor})
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

func (c *Client) versionUploadURL(ctx context.Context, clusterID, extensionID, versionID, label string) (*client.PresignedUrl, error) {
	const op = "cluster.extension.version.uploadUrl.create"
	resp, err := c.api.ClusterExtensionVersionUploadUrlCreateWithResponse(ctx, clusterID, extensionID, versionID,
		client.ExtensionVersionUploadUrlRequest{Label: &label})
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

func (c *Client) confirmVersion(ctx context.Context, clusterID, extensionID, versionID, resourceKey string) (*client.ExtensionVersion, error) {
	const op = "cluster.extension.version.confirm"
	resp, err := c.api.ClusterExtensionVersionConfirmWithResponse(ctx, clusterID, extensionID, versionID,
		client.ExtensionVersionLocationValidationRequest{ResourceKey: resourceKey})
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

// DeleteExtensionVersion removes one version of an extension.
func (c *Client) DeleteExtensionVersion(ctx context.Context, clusterID, extensionID, versionID string) error {
	const op = "cluster.extension.version.delete"
	resp, err := c.api.ClusterExtensionVersionDeleteWithResponse(ctx, clusterID, extensionID, versionID)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	return check(op, resp.StatusCode(), resp.Body)
}

// putObject uploads bytes to a presigned S3 target.
//
// This goes straight to S3, not to the API, so it deliberately uses a bare HTTP client with no
// OAuth editor attached — presigned URLs carry their own signature and an Authorization header
// makes S3 reject the request.
func (c *Client) putObject(ctx context.Context, p *client.PresignedUrl, body []byte) error {
	method := p.Method
	if method == "" {
		method = http.MethodPut
	}
	req, err := http.NewRequestWithContext(ctx, method, p.Url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building upload request: %w", err)
	}
	req.ContentLength = int64(len(body))

	hc := c.cfg.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("uploading to presigned URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("uploading to presigned URL: %s: %s",
			resp.Status, errorMessage(b))
	}
	return nil
}
