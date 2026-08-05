package reconcile_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Dawil/draiver/internal/reconcile"
	"github.com/Dawil/draiver/internal/store"
)

// TestControllerRoundTrip: the controller record writes, reads back equal, and
// clears — with a missing file reported as "no controller", not an error.
func TestControllerRoundTrip(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}

	if _, ok, err := reconcile.ReadController(root); err != nil || ok {
		t.Fatalf("no controller yet: ok=%v err=%v", ok, err)
	}

	want := reconcile.Controller{PID: 4321, Nonce: "deadbeef", Started: time.Unix(1000, 0).UTC()}
	if err := reconcile.WriteController(root, want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, ok, err := reconcile.ReadController(root)
	if err != nil || !ok {
		t.Fatalf("read back: ok=%v err=%v", ok, err)
	}
	if got.PID != want.PID || got.Nonce != want.Nonce {
		t.Fatalf("round-trip mismatch: got %+v want %+v", got, want)
	}

	if err := reconcile.RemoveController(root); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, ok, _ := reconcile.ReadController(root); ok {
		t.Fatal("controller record should be gone after remove")
	}
	// Removing an already-absent record is not an error.
	if err := reconcile.RemoveController(root); err != nil {
		t.Fatalf("remove idempotent: %v", err)
	}
}

// TestRunClaimsAndReleasesController: `ctl up`'s Run writes the controller pidfile
// on start and removes it on a clean (ctx-cancel) exit — the "PID 1 is up" handle
// the imperative verbs probe.
func TestRunClaimsAndReleasesController(t *testing.T) {
	w := newWorld(t)
	f := &factory{}
	r := w.reconciler(t, f, newProc())

	ctx, cancel := context.WithCancel(context.Background())
	ctrl := reconcile.Controller{PID: 77001, Nonce: "boot-1"}
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx, 20*time.Millisecond, ctrl) }()

	waitFor(t, "controller record written", func() bool {
		_, err := os.Stat(w.root.ControllerPath())
		return err == nil
	})
	got, ok, err := reconcile.ReadController(w.root)
	if err != nil || !ok || got.Nonce != ctrl.Nonce {
		t.Fatalf("controller record: got %+v ok=%v err=%v", got, ok, err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
	if _, err := os.Stat(w.root.ControllerPath()); !os.IsNotExist(err) {
		t.Fatalf("controller record should be removed on clean exit, stat err=%v", err)
	}
}
