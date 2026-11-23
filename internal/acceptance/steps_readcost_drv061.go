package acceptance

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/Dawil/draiver/internal/pyramid"
)

// steps_readcost_drv061.go realises drv-061's acceptance criteria
// (features/repo-hygiene/low-value-reads.feature): the slimmed .test-pyramid.yaml keeps
// its exact config, ignore files keep minified assets and BDD output out of searches, and
// AGENTS.md / changelog.d/README.md carry the cheap-reading guidance. It reads the real
// repo, so the checks cannot drift from what ships. No browser, no screenshots.

func init() {
	steps := map[string]StepFunc{
		"the repository's .test-pyramid.yaml":                                     drv061Given,
		"the repository's ignore files":                                           drv061Given,
		"the repository's agent guidance":                                         drv061Given,
		"it decodes to the pinned golden semantics":                               drv061ThenGolden,
		"it parses to the same pyramid as the golden":                             drv061ThenParseEqual,
		"it is under 120 lines and points at docs/test-pyramid.md for the prose":  drv061ThenSlim,
		`.rgignore and .ignore both exclude "internal/web/static/*.min.js"`:       drv061ThenIgnores("internal/web/static/*.min.js"),
		`.rgignore and .ignore both exclude "features/_artifacts/"`:               drv061ThenIgnores("features/_artifacts/"),
		`a ripgrep search for "confirm(" matches no minified asset`:               drv061ThenRipgrep,
		"AGENTS.md says not to read the generated CHANGELOG.md":                   drv061ThenContains("AGENTS.md", "Don't read `CHANGELOG.md`", "changelog.d/*.md"),
		"AGENTS.md says to grep then Read with offset/limit for large files":      drv061ThenContains("AGENTS.md", "500 lines", "offset/limit"),
		"changelog.d/README.md says the version comes from version.FromChangelog": drv061ThenContains("changelog.d/README.md", "version.FromChangelog"),
	}
	for text, fn := range steps {
		registry[text] = fn
	}
}

const drv061Golden = "internal/pyramid/testdata/repo-pyramid.golden.json"

func drv061Given(w *World, sr *StepRun) error {
	root, err := ovRepoRoot()
	if err != nil {
		return err
	}
	w.set("drv061root", root)
	return nil
}

func drv061Read(w *World, rel string) ([]byte, error) {
	root, _ := w.get("drv061root").(string)
	if root == "" {
		return nil, fmt.Errorf("no repo root: a Given step must run first")
	}
	return os.ReadFile(filepath.Join(root, rel))
}

func drv061ThenGolden(w *World, sr *StepRun) error {
	data, err := drv061Read(w, pyramid.FileName)
	if err != nil {
		return err
	}
	got, err := pyramid.Semantics(data)
	if err != nil {
		return err
	}
	want, err := drv061Read(w, drv061Golden)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, want) {
		return fmt.Errorf("%s no longer decodes to %s (regenerate with UPDATE_GOLDEN=1 go test ./internal/pyramid only if the config change is intended)", pyramid.FileName, drv061Golden)
	}
	attachText(sr, "pyramid-semantics", fmt.Sprintf("%s (%d bytes) decodes byte-identically to %s:\n\n%s", pyramid.FileName, len(data), drv061Golden, got))
	return nil
}

func drv061ThenParseEqual(w *World, sr *StepRun) error {
	data, err := drv061Read(w, pyramid.FileName)
	if err != nil {
		return err
	}
	golden, err := drv061Read(w, drv061Golden)
	if err != nil {
		return err
	}
	a, err := pyramid.Parse(data)
	if err != nil {
		return err
	}
	b, err := pyramid.Parse(golden)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(a, b) {
		return fmt.Errorf("Parse(%s) differs from Parse(golden)", pyramid.FileName)
	}
	return nil
}

func drv061ThenSlim(w *World, sr *StepRun) error {
	data, err := drv061Read(w, pyramid.FileName)
	if err != nil {
		return err
	}
	if n := bytes.Count(data, []byte("\n")); n >= 120 {
		return fmt.Errorf("%s is %d lines; its prose belongs in docs/test-pyramid.md", pyramid.FileName, n)
	}
	if !bytes.Contains(data, []byte("docs/test-pyramid.md")) {
		return fmt.Errorf("%s does not point at docs/test-pyramid.md", pyramid.FileName)
	}
	doc, err := drv061Read(w, "docs/test-pyramid.md")
	if err != nil {
		return err
	}
	if !bytes.Contains(doc, []byte("## This repo's pyramid")) {
		return fmt.Errorf("docs/test-pyramid.md has no \"This repo's pyramid\" section")
	}
	return nil
}

func drv061ThenIgnores(pattern string) StepFunc {
	return func(w *World, sr *StepRun) error {
		for _, f := range []string{".rgignore", ".ignore"} {
			data, err := drv061Read(w, f)
			if err != nil {
				return err
			}
			found := false
			for _, line := range strings.Split(string(data), "\n") {
				if strings.TrimSpace(line) == pattern {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("%s does not list %q", f, pattern)
			}
			attachText(sr, f, string(data))
		}
		return nil
	}
}

// drv061ThenRipgrep runs the real search the spec measured (drv-044's `confirm(`
// returned 49.8KB from htmx.min.js). ripgrep is optional on a box; without it the
// ignore-file assertions above still hold the line.
func drv061ThenRipgrep(w *World, sr *StepRun) error {
	if _, err := exec.LookPath("rg"); err != nil {
		attachText(sr, "ripgrep", "rg not installed on this box; the ignore-file assertions cover it\n")
		return nil
	}
	root, _ := w.get("drv061root").(string)
	cmd := exec.Command("rg", "-l", "--fixed-strings", "confirm(")
	cmd.Dir = root
	out, _ := cmd.Output() // exit 1 == no matches, which is fine
	for _, f := range strings.Fields(string(out)) {
		if strings.HasSuffix(f, ".min.js") || strings.HasPrefix(f, "features/_artifacts/") {
			return fmt.Errorf("rg matched %s despite .rgignore", f)
		}
	}
	attachText(sr, "ripgrep", "$ rg -l --fixed-strings 'confirm('\n"+string(out))
	return nil
}

func drv061ThenContains(rel string, needles ...string) StepFunc {
	return func(w *World, sr *StepRun) error {
		data, err := drv061Read(w, rel)
		if err != nil {
			return err
		}
		for _, n := range needles {
			if !bytes.Contains(data, []byte(n)) {
				return fmt.Errorf("%s does not contain %q", rel, n)
			}
		}
		attachText(sr, rel, fmt.Sprintf("%s contains: %q\n", rel, needles))
		return nil
	}
}
