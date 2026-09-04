package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/p2-inc/terraform-provider-phasetwo/internal/client"
)

const activeClusterJSON = `{
  "id": "c1", "name": "demo", "host": "https://demo.global.auth.ac",
  "region": "US_EAST_1", "status": "ACTIVE", "tier": "starter",
  "resource_limits": "standard", "created_at": 1740000000000, "owner": "org1"
}`

// CreateCluster has to pick apart a three-way union that the spec models as five optional
// fields. Only the `cluster` shape is usable from Terraform; the other two must become errors
// that say what to do, and the 3DS one must not lose the cluster it created.
func TestCreateClusterUnionShapes(t *testing.T) {
	t.Run("cluster present is the success path", func(t *testing.T) {
		c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"requires_action":false,"cluster":` + activeClusterJSON + `}`))
		}))
		got, err := c.CreateCluster(context.Background(), client.DedicatedClusterRequest{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Id != "c1" {
			t.Errorf("cluster id = %q, want c1", got.Id)
		}
	})

	t.Run("link means checkout is required", func(t *testing.T) {
		c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"requires_action":false,"link":{"link":"https://checkout.stripe.com/x"}}`))
		}))
		_, err := c.CreateCluster(context.Background(), client.DedicatedClusterRequest{})
		if !errors.Is(err, ErrCheckoutRequired) {
			t.Fatalf("error = %v, want ErrCheckoutRequired", err)
		}
	})

	t.Run("requires_action carries the created cluster id", func(t *testing.T) {
		c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"requires_action":true,"cluster_id":"c9","client_secret":"pi_x"}`))
		}))
		_, err := c.CreateCluster(context.Background(), client.DedicatedClusterRequest{})

		var actionErr *ErrPaymentActionRequired
		if !errors.As(err, &actionErr) {
			t.Fatalf("error = %v, want *ErrPaymentActionRequired", err)
		}
		if actionErr.CreatedClusterID != "c9" {
			t.Errorf("CreatedClusterID = %q, want c9 — losing this orphans a billing cluster",
				actionErr.CreatedClusterID)
		}
	})

	t.Run("none of the three shapes is an error, not a nil deref", func(t *testing.T) {
		c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"requires_action":false}`))
		}))
		if _, err := c.CreateCluster(context.Background(), client.DedicatedClusterRequest{}); err == nil {
			t.Fatal("expected an error when the response matches no documented shape")
		}
	})
}

func TestClusterGone(t *testing.T) {
	gone := []client.ClusterState{ClusterArchived, ClusterPendingDeletion}
	present := []client.ClusterState{
		ClusterActive, ClusterProvisioning, ClusterBillingSetup,
		ClusterPendingPayment, ClusterDisabled, ClusterBillingRequired, ClusterSetupException,
	}
	for _, s := range gone {
		if !ClusterGone(s) {
			t.Errorf("ClusterGone(%s) = false, want true", s)
		}
	}
	for _, s := range present {
		if ClusterGone(s) {
			t.Errorf("ClusterGone(%s) = true, want false", s)
		}
	}
}

// A cluster that never settles payment will never provision, so waiting the full timeout would
// just hide the reason. It should fail fast and say why.
func TestWaitForClusterActiveFailsFastOnBillingStates(t *testing.T) {
	for _, state := range []string{"BILLING_SETUP", "PENDING_PAYMENT"} {
		t.Run(state, func(t *testing.T) {
			c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"c1","name":"demo","host":"h","region":"US_EAST_1",` +
					`"status":"` + state + `","tier":"starter","resource_limits":"standard",` +
					`"created_at":1}`))
			}))
			_, err := c.WaitForClusterActive(context.Background(), "c1", time0)
			if err == nil {
				t.Fatal("expected an immediate error")
			}
			if !strings.Contains(err.Error(), "payment") {
				t.Errorf("error should explain the payment problem, got: %v", err)
			}
		})
	}
}

func TestWaitForClusterActiveSucceeds(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(activeClusterJSON))
	}))
	got, err := c.WaitForClusterActive(context.Background(), "c1", time0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Status != ClusterActive {
		t.Errorf("status = %s, want ACTIVE", got.Status)
	}
}

// WaitForClusterGone treats a 404 as success, since a cluster that never billed is removed
// outright rather than scheduled.
func TestWaitForClusterGoneOn404(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	last, err := c.WaitForClusterGone(context.Background(), "c1", time0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if last != "" {
		t.Errorf("last state = %q, want empty for a cluster that was never seen", last)
	}
}

// The PENDING_DELETION case is the one worth reporting to the user, so it must come back as the
// observed state rather than being flattened into "gone".
func TestWaitForClusterGoneReportsPendingDeletion(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","name":"demo","host":"h","region":"US_EAST_1",` +
			`"status":"PENDING_DELETION","tier":"starter","resource_limits":"standard","created_at":1}`))
	}))
	last, err := c.WaitForClusterGone(context.Background(), "c1", time0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if last != ClusterPendingDeletion {
		t.Errorf("last state = %q, want PENDING_DELETION", last)
	}
}
