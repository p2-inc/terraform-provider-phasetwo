package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// time0 makes the wait helpers do a single pass with no sleeping, so tests stay fast. The
// helpers check the deadline after the first poll, so a zero timeout still gets one look.
const time0 = time.Duration(0)

func TestIsRestartConflict(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"restart workflow in flight", http.StatusConflict,
			`{"error":"A restart workflow is already in flight for cluster demo"}`, true},
		{"tier limit is also a 409 but must not be retried", http.StatusConflict,
			`{"error":"Clusters are limited to 5 deployments"}`, false},
		{"name taken is also a 409", http.StatusConflict,
			`{"error":"Name is taken or reserved."}`, false},
		{"not a conflict at all", http.StatusInternalServerError,
			`{"error":"restart"}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := check("op", tt.status, []byte(tt.body))
			if got := isRestartConflict(err); got != tt.want {
				t.Errorf("isRestartConflict(%q) = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}

// A 409 that says a restart is in flight should be waited out and retried, not surfaced.
func TestDoRestartingRetriesAfterConflict(t *testing.T) {
	var attempts int32
	var restarting atomic.Bool
	restarting.Store(true)

	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/restart-status") {
			w.Header().Set("Content-Type", "application/json")
			if restarting.Load() {
				restarting.Store(false) // clears on the next poll
				_, _ = w.Write([]byte(`{"restart_in_progress":true}`))
				return
			}
			_, _ = w.Write([]byte(`{"restart_in_progress":false}`))
			return
		}
		w.WriteHeader(http.StatusTeapot)
	}))

	err := c.DoRestarting(context.Background(), "c1", func() error {
		n := atomic.AddInt32(&attempts, 1)
		if n == 1 {
			return check("op", http.StatusConflict,
				[]byte(`{"error":"A restart workflow is already in flight for cluster demo"}`))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Errorf("attempts = %d, want 2 (one conflict, then a retry)", got)
	}
}

// A 409 that is not about restarts — a tier limit, say — must fail immediately. Retrying would
// hammer the API and then report a confusing "still in flight" error.
func TestDoRestartingDoesNotRetryOtherConflicts(t *testing.T) {
	var attempts int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/restart-status") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"restart_in_progress":false}`))
			return
		}
		w.WriteHeader(http.StatusTeapot)
	}))

	wantErr := check("op", http.StatusConflict, []byte(`{"error":"Clusters are limited to 5 deployments"}`))
	err := c.DoRestarting(context.Background(), "c1", func() error {
		atomic.AddInt32(&attempts, 1)
		return wantErr
	})
	if err == nil {
		t.Fatal("expected the error to propagate")
	}
	if !errors.Is(err, wantErr) && err.Error() != wantErr.Error() {
		t.Errorf("error = %v, want the original conflict", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Errorf("attempts = %d, want 1 — a tier limit must not be retried", got)
	}
}

// Terraform applies independent resources concurrently, so several env vars on one cluster would
// otherwise fire at once and have all but one rejected. The gate must serialize them.
func TestDoRestartingSerializesPerCluster(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/restart-status") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"restart_in_progress":false}`))
			return
		}
		w.WriteHeader(http.StatusTeapot)
	}))

	var mu sync.Mutex
	var concurrent, maxConcurrent int

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.DoRestarting(context.Background(), "same-cluster", func() error {
				mu.Lock()
				concurrent++
				if concurrent > maxConcurrent {
					maxConcurrent = concurrent
				}
				mu.Unlock()

				time.Sleep(2 * time.Millisecond)

				mu.Lock()
				concurrent--
				mu.Unlock()
				return nil
			})
		}()
	}
	wg.Wait()

	if maxConcurrent != 1 {
		t.Errorf("max concurrent calls = %d, want 1 — changes to one cluster must be serialized",
			maxConcurrent)
	}
}

// Different clusters have independent restart state, so they must not block each other.
func TestDoRestartingAllowsDifferentClustersInParallel(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/restart-status") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"restart_in_progress":false}`))
			return
		}
		w.WriteHeader(http.StatusTeapot)
	}))

	release := make(chan struct{})
	entered := make(chan struct{}, 2)

	for _, id := range []string{"cluster-a", "cluster-b"} {
		go func(id string) {
			_ = c.DoRestarting(context.Background(), id, func() error {
				entered <- struct{}{}
				<-release
				return nil
			})
		}(id)
	}

	// Both should get in without either finishing.
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			close(release)
			t.Fatal("a call on one cluster blocked a call on another")
		}
	}
	close(release)
}

func TestRestartInProgress(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"restart_in_progress":true}`))
	}))
	got, err := c.RestartInProgress(context.Background(), "c1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("RestartInProgress = false, want true")
	}
}
