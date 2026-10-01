package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoad_MissingFileIsDefaults(t *testing.T) {
	// A path that does not exist is not an error — defaults apply.
	c, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	// Config now carries a map (Permissions), so compare structurally.
	if !reflect.DeepEqual(c, Default()) {
		t.Fatalf("got %+v, want defaults %+v", c, Default())
	}
	if c.ContextLimit != DefaultContextLimit {
		t.Fatalf("default limit = %d, want %d", c.ContextLimit, DefaultContextLimit)
	}
}

func TestLoad_PartialFileFillsDefaults(t *testing.T) {
	path := writeConfig(t, `{"context_limit": 120000}`)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.ContextLimit != 120000 {
		t.Errorf("limit = %d, want 120000 (from file)", c.ContextLimit)
	}
	if c.ContextWindow != DefaultContextWindow {
		t.Errorf("window = %d, want default %d (omitted field)", c.ContextWindow, DefaultContextWindow)
	}
}

func TestLoad_ExplicitZeroDisables(t *testing.T) {
	// An explicit 0 must survive — it is how an operator disables the auto-stop —
	// and must not be mistaken for "omitted → default".
	path := writeConfig(t, `{"context_limit": 0}`)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.ContextLimit != 0 {
		t.Errorf("limit = %d, want 0 (explicit disable, not defaulted)", c.ContextLimit)
	}
}

func TestLoad_UnparseableFileErrors(t *testing.T) {
	path := writeConfig(t, `{not json`)
	if _, err := Load(path); err == nil {
		t.Fatal("a present-but-broken config should error, not silently default")
	}
}

func TestLoad_Permissions(t *testing.T) {
	path := writeConfig(t, `{"permissions_default": "escalate", "permissions": {"Read": "allow", "WebFetch": "escalate"}}`)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.PermissionsDefault != "escalate" {
		t.Errorf("permissions_default = %q, want escalate", c.PermissionsDefault)
	}
	want := map[string]string{"Read": "allow", "WebFetch": "escalate"}
	if !reflect.DeepEqual(c.Permissions, want) {
		t.Errorf("permissions = %+v, want %+v", c.Permissions, want)
	}
	// Context fields still take their defaults when omitted.
	if c.ContextLimit != DefaultContextLimit {
		t.Errorf("limit = %d, want default %d", c.ContextLimit, DefaultContextLimit)
	}
}

func TestLoad_PermissionsOmittedIsNil(t *testing.T) {
	// No permissions key → nil map / empty default, which the caller reads as "no
	// override" (the allow-all base shows through).
	path := writeConfig(t, `{"context_limit": 120000}`)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Permissions != nil {
		t.Errorf("permissions = %+v, want nil (omitted)", c.Permissions)
	}
	if c.PermissionsDefault != "" {
		t.Errorf("permissions_default = %q, want empty (omitted)", c.PermissionsDefault)
	}
}

func TestLoad_ReviewLinkHosts(t *testing.T) {
	// Omitted → nil (empty = any host, the opt-in-off default).
	c, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatal(err)
	}
	if c.ReviewLinkHosts != nil {
		t.Errorf("default ReviewLinkHosts = %v, want nil", c.ReviewLinkHosts)
	}

	// Set → used verbatim, alongside the existing Permissions field.
	path := writeConfig(t, `{"review_link_hosts": ["bitbucket.mycorp.com", "github.com"]}`)
	c, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.ReviewLinkHosts, []string{"bitbucket.mycorp.com", "github.com"}) {
		t.Errorf("ReviewLinkHosts = %v, want the two configured hosts", c.ReviewLinkHosts)
	}
}

func TestLoad_PrimaryRemote(t *testing.T) {
	// Omitted → empty (unconfigured; the agent falls back to the sole remote or
	// surfaces the ambiguity when there are several).
	c, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatal(err)
	}
	if c.PrimaryRemote != "" {
		t.Errorf("default PrimaryRemote = %q, want empty (omitted)", c.PrimaryRemote)
	}

	// Set → used verbatim.
	path := writeConfig(t, `{"primary_remote": "forgejo"}`)
	c, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.PrimaryRemote != "forgejo" {
		t.Errorf("PrimaryRemote = %q, want %q", c.PrimaryRemote, "forgejo")
	}
}

func TestLoad_DefaultSupervision(t *testing.T) {
	// Omitted → the built-in floor, passthrough (drvctl-042): opting a fleet into
	// pre-digest is a deliberate config choice, mirroring how enable is opt-in.
	c, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatal(err)
	}
	if c.DefaultSupervision != DefaultSupervision {
		t.Errorf("default DefaultSupervision = %q, want %q (the floor)", c.DefaultSupervision, DefaultSupervision)
	}
	if DefaultSupervision != "passthrough" {
		t.Errorf("built-in DefaultSupervision = %q, want passthrough", DefaultSupervision)
	}

	// Set → used verbatim (validated into a project.Supervision at the reconciler
	// edge, not here — this package stays dependency-free).
	path := writeConfig(t, `{"default_supervision": "pre-digest"}`)
	c, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.DefaultSupervision != "pre-digest" {
		t.Errorf("DefaultSupervision = %q, want %q (from file)", c.DefaultSupervision, "pre-digest")
	}
}

func TestLoad_PromptCacheSurface(t *testing.T) {
	// Omitted → the byte-identical-no-op default: toggle off, no append file.
	c, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatal(err)
	}
	if c.ExcludeDynamicSystemPromptSections {
		t.Errorf("default ExcludeDynamicSystemPromptSections = true, want false")
	}
	if c.AppendSystemPromptFile != "" {
		t.Errorf("default AppendSystemPromptFile = %q, want empty", c.AppendSystemPromptFile)
	}

	// Set → used verbatim.
	path := writeConfig(t, `{"exclude_dynamic_system_prompt_sections": true, "append_system_prompt_file": "/etc/draiver/protocol.txt"}`)
	c, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !c.ExcludeDynamicSystemPromptSections {
		t.Errorf("ExcludeDynamicSystemPromptSections = false, want true (from file)")
	}
	if c.AppendSystemPromptFile != "/etc/draiver/protocol.txt" {
		t.Errorf("AppendSystemPromptFile = %q, want the configured path", c.AppendSystemPromptFile)
	}
}

func TestLoad_ReposOmittedIsNil(t *testing.T) {
	// No repos key → nil map, read as "no per-repo overrides".
	c, err := Load(writeConfig(t, `{"primary_remote": "forgejo"}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Repos != nil {
		t.Errorf("Repos = %+v, want nil (omitted)", c.Repos)
	}
}

func TestLoad_ReposParsed(t *testing.T) {
	path := writeConfig(t, `{
	  "primary_remote": "origin",
	  "repos": {
	    "/home/dev/app": {"default_remote": "forgejo", "default_branch": "main", "default_test_rung": "integration"},
	    "/home/dev/lib": {"default_branch": "trunk"}
	  }
	}`)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]RepoSettings{
		"/home/dev/app": {DefaultRemote: "forgejo", DefaultBranch: "main", DefaultTestRung: "integration"},
		"/home/dev/lib": {DefaultBranch: "trunk"},
	}
	if !reflect.DeepEqual(c.Repos, want) {
		t.Errorf("Repos = %+v, want %+v", c.Repos, want)
	}
}

func TestRepoSettings_ResolutionOrder(t *testing.T) {
	// repo → global → builtin, each key independently. The app repo sets its own
	// remote (overriding primary_remote) and leaves branch/rung unset; the lib repo
	// sets nothing, so every key falls through.
	c, err := Load(writeConfig(t, `{
	  "primary_remote": "origin",
	  "repos": {"/home/dev/app": {"default_remote": "forgejo"}}
	}`))
	if err != nil {
		t.Fatal(err)
	}

	app := c.RepoSettings("/home/dev/app")
	if app.Remote != "forgejo" {
		t.Errorf("app remote = %q, want forgejo (per-repo overrides global)", app.Remote)
	}
	if app.Branch != "" || app.TestRung != "" {
		t.Errorf("app branch/rung = %q/%q, want empty (builtin falls through to caller)", app.Branch, app.TestRung)
	}

	// A repo with no entry: remote falls to the global primary_remote, branch/rung to
	// the builtin (empty — the caller's git branch / file top rung).
	lib := c.RepoSettings("/home/dev/lib")
	if lib.Remote != "origin" {
		t.Errorf("lib remote = %q, want origin (global fallback)", lib.Remote)
	}
	if lib.Branch != "" || lib.TestRung != "" {
		t.Errorf("lib branch/rung = %q/%q, want empty", lib.Branch, lib.TestRung)
	}

	// No global primary_remote and no entry → fully unset.
	bare, _ := Load(filepath.Join(t.TempDir(), "nope.json"))
	if r := bare.RepoSettings("/x"); r.Remote != "" || r.Branch != "" || r.TestRung != "" {
		t.Errorf("bare resolve = %+v, want all empty", r)
	}
}

func TestSetRepoSettings_MergePreservesUnrelatedKeys(t *testing.T) {
	// A file carrying global tunables (including an explicit context_limit: 0 that must
	// survive the round-trip) and one repo's settings. A write to a DIFFERENT repo must
	// leave every one of those untouched.
	path := writeConfig(t, `{
	  "context_limit": 0,
	  "primary_remote": "forgejo",
	  "permissions": {"WebFetch": "escalate"},
	  "repos": {"/home/dev/app": {"default_remote": "origin", "default_branch": "main"}}
	}`)

	remote := "gitlab"
	changed, err := SetRepoSettings(path, "/home/dev/lib", RepoSettingsUpdate{DefaultRemote: &remote})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}

	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.ContextLimit != 0 {
		t.Errorf("context_limit = %d, want 0 (explicit-zero preserved through the merge)", c.ContextLimit)
	}
	if c.PrimaryRemote != "forgejo" {
		t.Errorf("primary_remote = %q, want forgejo (preserved)", c.PrimaryRemote)
	}
	if !reflect.DeepEqual(c.Permissions, map[string]string{"WebFetch": "escalate"}) {
		t.Errorf("permissions = %+v, want preserved", c.Permissions)
	}
	if app := c.Repos["/home/dev/app"]; app.DefaultRemote != "origin" || app.DefaultBranch != "main" {
		t.Errorf("app settings = %+v, want untouched", app)
	}
	if lib := c.Repos["/home/dev/lib"]; lib.DefaultRemote != "gitlab" {
		t.Errorf("lib remote = %q, want gitlab (the write)", lib.DefaultRemote)
	}
}

func TestSetRepoSettings_PartialUpdateLeavesOtherKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")

	branch := "main"
	if _, err := SetRepoSettings(path, "/r", RepoSettingsUpdate{DefaultBranch: &branch}); err != nil {
		t.Fatal(err)
	}
	// A second write touching only the rung must not blank the branch.
	rung := "e2e"
	if _, err := SetRepoSettings(path, "/r", RepoSettingsUpdate{DefaultTestRung: &rung}); err != nil {
		t.Fatal(err)
	}

	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := c.Repos["/r"]
	if got.DefaultBranch != "main" || got.DefaultTestRung != "e2e" {
		t.Errorf("settings = %+v, want branch=main rung=e2e (partial updates accrete)", got)
	}
}

func TestSetRepoSettings_ClearKeyFallsBack(t *testing.T) {
	path := writeConfig(t, `{"primary_remote": "forgejo", "repos": {"/r": {"default_remote": "origin"}}}`)

	// An explicit empty value clears the key, so the resolver falls back to the global.
	empty := ""
	if _, err := SetRepoSettings(path, "/r", RepoSettingsUpdate{DefaultRemote: &empty}); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Repos["/r"].DefaultRemote; got != "" {
		t.Errorf("stored default_remote = %q, want empty (cleared)", got)
	}
	if r := c.RepoSettings("/r"); r.Remote != "forgejo" {
		t.Errorf("resolved remote = %q, want forgejo (fell back after clear)", r.Remote)
	}
}

func TestSetRepoSettings_NothingToSetWritesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	changed, err := SetRepoSettings(path, "/r", RepoSettingsUpdate{})
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("changed = true, want false for an empty update")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file exists after a no-op update (err=%v), want no file written", err)
	}
}

func TestSetRepoSettings_CreatesMissingFile(t *testing.T) {
	// First write mints config.json (and its parent dir) — the opt-in-file discipline.
	path := filepath.Join(t.TempDir(), "sub", "config.json")
	remote := "forgejo"
	if _, err := SetRepoSettings(path, "/r", RepoSettingsUpdate{DefaultRemote: &remote}); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Repos["/r"].DefaultRemote != "forgejo" {
		t.Errorf("default_remote = %q, want forgejo", c.Repos["/r"].DefaultRemote)
	}
}

func TestSetRepoSettings_OneWriteSeenByEveryReader(t *testing.T) {
	// The store is the single global config, so two readers keyed by the SAME repo path
	// (standing in for two attempts/tickets on that repo) observe one write — the core
	// value of per-repo settings living in config, not per-attempt attempt.md.
	path := filepath.Join(t.TempDir(), "config.json")
	rung := "integration"
	if _, err := SetRepoSettings(path, "/home/dev/app", RepoSettingsUpdate{DefaultTestRung: &rung}); err != nil {
		t.Fatal(err)
	}
	for _, reader := range []string{"reader-A", "reader-B"} {
		c, err := Load(path) // each reader loads the one config independently
		if err != nil {
			t.Fatalf("%s: %v", reader, err)
		}
		if got := c.RepoSettings("/home/dev/app").TestRung; got != "integration" {
			t.Errorf("%s resolved rung = %q, want integration", reader, got)
		}
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
