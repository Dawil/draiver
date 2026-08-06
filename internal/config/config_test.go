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

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
