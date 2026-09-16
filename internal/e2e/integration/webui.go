//go:build integration

package integration

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Webui is the real `draiver webui` binary, served over HTTP against a seeded
// data root on a free loopback port. Driving the actual binary (rather than
// wiring internal/web in-process) keeps the harness decoupled from web internals
// and exercises the same command an operator runs. The process is bound to the
// test via t.Cleanup.
type Webui struct {
	URL string // e.g. http://127.0.0.1:54321
	t   *testing.T
	cmd *exec.Cmd
}

// ServeWebui builds (once) and launches the served binary over d, waiting until
// GET / answers 200 before returning — the startup banner is printed before the
// listener binds, so polling the port is the reliable readiness signal.
func ServeWebui(t *testing.T, d *DataRoot) *Webui {
	t.Helper()
	addr := freeLoopbackAddr(t)
	cmd := exec.Command(d.bin, "--data", d.Dir, "webui", "--addr", addr, "--actor", "human:harness")
	cmd.Env = append(os.Environ(), "DRAIVER_ACTOR=human:harness")
	// Surface server logs on failure without spamming a passing run.
	var logs strings.Builder
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("start webui: %v", err)
	}
	w := &Webui{URL: "http://" + addr, t: t, cmd: cmd}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	if err := waitForHTTP(w.URL+"/", 20*time.Second); err != nil {
		t.Fatalf("webui did not become ready: %v\nserver logs:\n%s", err, logs.String())
	}
	return w
}

// Get fetches a path and returns the status and body, failing the test on a
// transport error.
func (w *Webui) Get(path string) (int, string) {
	w.t.Helper()
	resp, err := http.Get(w.URL + path)
	if err != nil {
		w.t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		w.t.Fatalf("read %s body: %v", path, err)
	}
	return resp.StatusCode, string(body)
}

// GetOK fetches a path, asserts a 200, and returns the body.
func (w *Webui) GetOK(path string) string {
	w.t.Helper()
	code, body := w.Get(path)
	if code != http.StatusOK {
		w.t.Fatalf("GET %s = %d, want 200\n%s", path, code, body)
	}
	return body
}

// freeLoopbackAddr asks the kernel for an unused loopback port and returns it as
// host:port. Closing the probe listener before the child binds leaves a small
// race window, acceptable for a test harness on a quiet CI host.
func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer l.Close()
	return l.Addr().String()
}

func waitForHTTP(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := http.DefaultClient.Do(req)
		cancel()
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			last = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			last = err
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("not ready after %s: %w", timeout, last)
}
