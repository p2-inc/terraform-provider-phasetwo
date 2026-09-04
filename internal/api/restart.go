package api

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Changing an environment variable or reconciling extensions restarts the cluster's Keycloak
// deployment, and the API rejects a second such change with 409 while one is in flight. Two
// things are needed to make that survivable from Terraform:
//
//  1. A per-cluster lock. Terraform applies independent resources concurrently, so a config with
//     several env vars on one cluster would otherwise fire them all at once and have all but one
//     fail. The lock is per-process, which covers the ordinary single-apply case.
//  2. A wait-and-retry on 409. The lock cannot see restarts started by anyone else — another
//     operator, the console, a second Terraform run — so a 409 is still possible and is treated
//     as "wait for the current restart, then try again" rather than as a failure.

const (
	restartPollInterval = 5 * time.Second
	// restartWaitTimeout bounds a single wait. A Keycloak restart is normally well under a
	// minute; this is generous enough to absorb a slow rollout without hanging an apply forever.
	restartWaitTimeout = 15 * time.Minute
	// restartRetries is how many times a mutating call is re-attempted after a 409.
	restartRetries = 4
)

type restartGate struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func newRestartGate() *restartGate {
	return &restartGate{locks: make(map[string]*sync.Mutex)}
}

func (g *restartGate) lockFor(clusterID string) *sync.Mutex {
	g.mu.Lock()
	defer g.mu.Unlock()
	l, ok := g.locks[clusterID]
	if !ok {
		l = &sync.Mutex{}
		g.locks[clusterID] = l
	}
	return l
}

// RestartInProgress reports whether the cluster is currently restarting.
func (c *Client) RestartInProgress(ctx context.Context, clusterID string) (bool, error) {
	resp, err := c.api.ClusterRestartStatusDetailWithResponse(ctx, clusterID)
	if err != nil {
		return false, fmt.Errorf("cluster.restartStatus.detail: %w", err)
	}
	if err := check("cluster.restartStatus.detail", resp.StatusCode(), resp.Body); err != nil {
		return false, err
	}
	if resp.JSON200 == nil {
		return false, missingBody("cluster.restartStatus.detail", resp.StatusCode())
	}
	return resp.JSON200.RestartInProgress, nil
}

// WaitForRestart blocks until no restart is in flight for the cluster.
func (c *Client) WaitForRestart(ctx context.Context, clusterID string) error {
	deadline := time.Now().Add(restartWaitTimeout)
	for {
		inProgress, err := c.RestartInProgress(ctx, clusterID)
		if err != nil {
			// A cluster that has gone away is not going to finish restarting. Let the caller's
			// own read decide what that means.
			if IsNotFound(err) {
				return err
			}
			return err
		}
		if !inProgress {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for cluster %s to finish restarting",
				restartWaitTimeout, clusterID)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(restartPollInterval):
		}
	}
}

// DoRestarting runs a mutating call that triggers a cluster restart, serialized against other
// such calls for the same cluster and retried when the API reports one already in flight.
//
// fn may run more than once, so it must be safe to retry. Every operation this is used for
// (create/update/delete of an env var, extension reconcile) either succeeds or has no effect,
// because the 409 is raised before any state change.
func (c *Client) DoRestarting(ctx context.Context, clusterID string, fn func() error) error {
	l := c.restarts.lockFor(clusterID)
	l.Lock()
	defer l.Unlock()

	// Clear any restart already running before the first attempt, so the common case costs one
	// wait rather than a failed call plus a wait.
	if err := c.WaitForRestart(ctx, clusterID); err != nil && !IsNotFound(err) {
		return err
	}

	var lastErr error
	for attempt := 0; attempt <= restartRetries; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}
		if !isRestartConflict(err) {
			return err
		}
		lastErr = err

		if waitErr := c.WaitForRestart(ctx, clusterID); waitErr != nil {
			return waitErr
		}
	}
	return fmt.Errorf("cluster %s still had a restart in flight after %d attempts: %w",
		clusterID, restartRetries+1, lastErr)
}

// isRestartConflict distinguishes "a restart is already running" from the other things the API
// reports as 409 — tier limits, duplicate names — which retrying would not fix.
func isRestartConflict(err error) bool {
	if !IsConflict(err) {
		return false
	}
	var e *Error
	if !asError(err, &e) {
		return false
	}
	m := strings.ToLower(e.Message)
	return strings.Contains(m, "restart") || strings.Contains(m, "already in flight") ||
		strings.Contains(m, "workflow")
}
