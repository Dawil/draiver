package pyramid

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// repo_pyramid_test.go (drv-061) pins the SEMANTICS of this repo's own
// .test-pyramid.yaml. drv-061 moved its explanatory prose into docs/test-pyramid.md;
// comments are free to change, but the config must not. The golden is the file decoded
// generically (every key, not only those Pyramid models) and re-encoded as JSON, so a
// reworded comment passes and any change to a value, key or ordering fails.
//
// Changed the config on purpose? Regenerate the golden and commit it with the change:
//
//	UPDATE_GOLDEN=1 go test ./internal/pyramid -run TestRepoPyramidSemantics

const repoPyramidGolden = "testdata/repo-pyramid.golden.json"

func TestRepoPyramidSemantics(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", FileName))
	if err != nil {
		t.Fatalf("read repo pyramid: %v", err)
	}
	got, err := Semantics(data)
	if err != nil {
		t.Fatalf("decode repo pyramid: %v", err)
	}
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll(filepath.Dir(repoPyramidGolden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(repoPyramidGolden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(repoPyramidGolden)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s no longer decodes to %s — its config changed, not just its comments.\n"+
			"If intended, regenerate: UPDATE_GOLDEN=1 go test ./internal/pyramid -run TestRepoPyramidSemantics", FileName, repoPyramidGolden)
	}

	// And the typed config draiver actually runs on: Parse of the file equals Parse of
	// the golden (JSON is YAML), so the two are the same Pyramid.
	p, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse repo pyramid: %v", err)
	}
	pg, err := Parse(want)
	if err != nil {
		t.Fatalf("Parse golden: %v", err)
	}
	if !reflect.DeepEqual(p, pg) {
		t.Fatalf("Parse(%s) differs from Parse(golden)", FileName)
	}
}
