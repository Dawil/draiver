package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_MissingFileIsDefaults(t *testing.T) {
	// A path that does not exist is not an error — defaults apply.
	c, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if c != Default() {
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

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
