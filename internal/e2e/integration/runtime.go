//go:build integration

package integration

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/testcontainers/testcontainers-go"
)

// draiverBin builds the draiver binary once per test binary run and returns its
// path. Every seeder and the served webui shell out to this real binary (never
// `go run`, which would remap escalate's exit-3 halt to 1) — the same discipline
// e2e/global-setup.ts follows.
var draiverBin = sync.OnceValues(buildDraiver)

func buildDraiver() (string, error) {
	root, err := repoRoot()
	if err != nil {
		return "", err
	}
	bin := filepath.Join(root, "internal", "e2e", "integration", ".bin", "draiver")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", &buildError{stderr: stderr.String(), err: err}
	}
	return bin, nil
}

type buildError struct {
	stderr string
	err    error
}

func (e *buildError) Error() string {
	return "build draiver: " + e.err.Error() + "\n" + e.stderr
}

// DraiverBin builds (once) and returns the path to the draiver binary under test,
// failing the test if the build fails.
func DraiverBin(t *testing.T) string {
	t.Helper()
	bin, err := draiverBin()
	if err != nil {
		t.Fatalf("build draiver binary: %v", err)
	}
	return bin
}

// repoRoot walks up from this source file to the module root (the directory
// holding go.mod). Using runtime.Caller keeps the harness independent of the
// working directory a test happens to run from.
func repoRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", errNoCaller
	}
	// file is <root>/internal/e2e/integration/runtime.go
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..")), nil
}

var errNoCaller = &staticError{"cannot resolve repo root: runtime.Caller failed"}

type staticError struct{ msg string }

func (e *staticError) Error() string { return e.msg }

// RequireContainerRuntime skips the test cleanly, with an actionable message,
// when no container runtime is reachable — so the integration rung degrades to
// "run what needs no daemon, skip the rest" on a machine without Docker/Podman
// rather than failing. It probes Testcontainers' provider (which honours
// DOCKER_HOST, the default socket, and Podman) so the check matches what the
// container helpers will actually use.
func RequireContainerRuntime(t *testing.T) {
	t.Helper()
	provider, err := testcontainers.NewDockerProvider()
	if err != nil {
		t.Skipf("no container runtime: %v (integration forge tests need Docker/Podman; skipping)", err)
	}
	defer provider.Close()
	if err := provider.Health(context.Background()); err != nil {
		t.Skipf("container runtime not healthy: %v (is the Docker/Podman daemon running? skipping forge test)", err)
	}
}
