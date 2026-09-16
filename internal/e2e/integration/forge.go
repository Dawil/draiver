//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/wait"
)

// forgeImage is a Gitea image — a Forgejo-family forge, API- and git-over-HTTP
// compatible with what draiver's loosely-coupled-forge coupling talks to. Pinned
// for reproducibility.
const forgeImage = "gitea/gitea:1.22"

const (
	forgeUser = "tester"
	forgePass = "test-passw0rd"
)

// Forge is a running Forgejo/Gitea container — the review forge under test. It
// exposes git-over-HTTP and the v1 API on a mapped port; a test seeds a repo and
// pushes a "merged PR" state onto it, then drives `draiver ctl merge --remote`
// against it for a real containment check. The container is Terminated on
// cleanup (Testcontainers' Reaper is the backstop), so nothing leaks.
type Forge struct {
	BaseURL   string // http://host:port
	container testcontainers.Container
	t         *testing.T
}

// NewForge starts a forge container, waits until its HTTP API is serving, and
// creates an admin user. It skips the test cleanly when no container runtime is
// available.
func NewForge(t *testing.T) *Forge {
	t.Helper()
	RequireContainerRuntime(t)
	ctx := context.Background()

	req := testcontainers.ContainerRequest{
		Image:        forgeImage,
		ExposedPorts: []string{"3000/tcp"},
		Env: map[string]string{
			// INSTALL_LOCK skips the interactive installer so the server comes up
			// ready; sqlite keeps it single-container and dependency-free.
			"GITEA__security__INSTALL_LOCK": "true",
			"GITEA__database__DB_TYPE":      "sqlite3",
			"GITEA__log__LEVEL":             "error",
			// The board never signs in; disabling the requirement keeps API +
			// git-over-HTTP straightforward with basic auth.
			"GITEA__service__REQUIRE_SIGNIN_VIEW": "false",
		},
		WaitingFor: wait.ForHTTP("/api/v1/version").WithPort("3000/tcp").WithStartupTimeout(120 * time.Second),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start forge container: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = container.Terminate(ctx)
	})

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("forge host: %v", err)
	}
	port, err := container.MappedPort(ctx, "3000/tcp")
	if err != nil {
		t.Fatalf("forge mapped port: %v", err)
	}
	f := &Forge{
		BaseURL:   fmt.Sprintf("http://%s:%s", host, port.Port()),
		container: container,
		t:         t,
	}
	f.createAdmin(ctx)
	return f
}

// createAdmin provisions the admin user via the Gitea CLI inside the container.
// It runs as the `git` user (the image's service account) so DB files keep the
// right ownership, retrying briefly since first-boot migrations may still be
// settling when the HTTP wait clears.
func (f *Forge) createAdmin(ctx context.Context) {
	f.t.Helper()
	cmd := []string{
		"gitea", "admin", "user", "create",
		"--username", forgeUser,
		"--password", forgePass,
		"--email", forgeUser + "@draiver.test",
		"--admin", "--must-change-password=false",
	}
	deadline := time.Now().Add(60 * time.Second)
	var lastOut string
	for {
		code, reader, err := f.container.Exec(ctx, cmd, tcexec.WithUser("git"))
		if err == nil && code == 0 {
			return
		}
		if reader != nil {
			b, _ := io.ReadAll(reader)
			lastOut = string(b)
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("create forge admin (exit %d): %v\n%s", code, err, lastOut)
		}
		time.Sleep(1 * time.Second)
	}
}

// CreateRepo creates an empty repository owned by the admin user and returns its
// authenticated push/clone URL (credentials embedded, as a CI remote would use).
// auto_init is off so the test controls the entire commit graph via push.
func (f *Forge) CreateRepo(name string) string {
	f.t.Helper()
	body, _ := json.Marshal(map[string]any{
		"name":     name,
		"private":  false,
		"auto_init": false,
	})
	req, _ := http.NewRequest(http.MethodPost, f.BaseURL+"/api/v1/user/repos", bytes.NewReader(body))
	req.SetBasicAuth(forgeUser, forgePass)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatalf("create forge repo: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		f.t.Fatalf("create forge repo %q: status %d\n%s", name, resp.StatusCode, out)
	}
	return f.AuthURL(name)
}

// AuthURL is the git-over-HTTP URL for a repo with basic-auth credentials inlined.
func (f *Forge) AuthURL(name string) string {
	scheme, rest, _ := strings.Cut(f.BaseURL, "://")
	return fmt.Sprintf("%s://%s:%s@%s/%s/%s.git", scheme, forgeUser, forgePass, rest, forgeUser, name)
}
