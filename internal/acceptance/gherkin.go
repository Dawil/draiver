package acceptance

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// This file is a deliberately minimal Gherkin reader — enough to drive draiver's own
// acceptance features, not a full parser. It understands Feature/Rule/Background/
// Scenario, the step keywords (Given/When/Then/And/But), `@tags`, and the free-text
// Feature description. draiver consumes cucumber-JSON runner-agnostically, so the
// runner only needs to turn these features into the steps it executes and the JSON
// it emits (tags, rule grouping, folder URIs — the structure the drv-023 viewer
// turns into navigation), not to implement the whole Gherkin grammar.
//
// Backgrounds are *inlined*: a Background's steps are prepended to every scenario in
// its scope (the feature, or the enclosing Rule). The source stays DRY while each
// scenario still executes exactly the steps it did before extraction, and the
// emitted cucumber-JSON carries no standalone background element (which the viewer
// would otherwise render as a bare scenario).

// stepKeywords are the line prefixes that introduce a step. "And"/"But" continue
// the previous step's intent; we keep the literal keyword for display and match on
// the step text, so a registry entry is shared regardless of the leading keyword.
var stepKeywords = []string{"Given ", "When ", "Then ", "And ", "But "}

// skipDirs are directory names never walked for features: the runner entrypoint, the
// artefact output, vendored node_modules / testdata, and the screenshots helper.
var skipDirs = map[string]bool{
	"acceptance":   true, // features/acceptance — the godog-style runner's main
	"_artifacts":   true, // the runner's own output
	"screenshots":  true, // the Playwright helper (package.json + node_modules)
	"node_modules": true,
	"testdata":     true,
}

// GherkinStep is one parsed step: its leading keyword and the step text the
// registry is keyed on.
type GherkinStep struct {
	Keyword string
	Text    string
	Line    int
}

// GherkinRule is a `Rule:` block a scenario belongs to (drv-023 grouping). Scenarios
// with no enclosing rule carry a nil *GherkinRule and render flat.
type GherkinRule struct {
	Name string
	Line int
	Tags []string
}

// GherkinScenario is one scenario, its ordered steps (any enclosing Background's
// steps already prepended), its own tags, and the Rule it belongs to (nil if none).
type GherkinScenario struct {
	Name  string
	Line  int
	Tags  []string
	Rule  *GherkinRule
	Steps []GherkinStep
}

// GherkinFeature is one .feature file: its name, source URI, feature-level tags, the
// free-text description under `Feature:`, and its scenarios.
type GherkinFeature struct {
	Name        string
	URI         string
	Tags        []string
	Description string
	Scenarios   []GherkinScenario
}

// ParseFeaturesDir walks dir recursively (sorted by path for a stable run order) and
// returns every *.feature it finds, skipping the runner entrypoint, artefact output,
// and vendored dirs (see skipDirs) so only the capability folders are discovered.
func ParseFeaturesDir(dir string) ([]GherkinFeature, error) {
	var paths []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != dir && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".feature") {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var features []GherkinFeature
	for _, path := range paths {
		f, err := parseFeatureFile(path)
		if err != nil {
			return nil, err
		}
		features = append(features, f)
	}
	return features, nil
}

func parseFeatureFile(path string) (GherkinFeature, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return GherkinFeature{}, err
	}
	feat := GherkinFeature{URI: filepath.ToSlash(path)}

	var (
		pendingTags []string           // tags accumulated for the next Feature/Rule/Scenario
		descLines   []string           // free-text lines under Feature:
		inDesc      bool               // collecting the Feature description
		featureBg   []GherkinStep      // Background declared before any Rule
		ruleBg      []GherkinStep      // Background declared inside the current Rule
		curRule     *GherkinRule       // the Rule currently open, if any
		scenarios   []*GherkinScenario // pointers, so stepTarget stays valid across appends
		stepTarget  *[]GherkinStep     // where a step line appends: a background or a scenario
	)

	sc := bufio.NewScanner(strings.NewReader(string(data)))
	line := 0
	for sc.Scan() {
		line++
		raw := strings.TrimSpace(sc.Text())
		switch {
		case raw == "" || strings.HasPrefix(raw, "#"):
			continue
		case strings.HasPrefix(raw, "@"):
			pendingTags = append(pendingTags, parseTags(raw)...)
			inDesc = false
		case strings.HasPrefix(raw, "Feature:"):
			feat.Name = strings.TrimSpace(strings.TrimPrefix(raw, "Feature:"))
			feat.Tags = pendingTags
			pendingTags = nil
			inDesc = true
			stepTarget = nil
		case strings.HasPrefix(raw, "Rule:"):
			curRule = &GherkinRule{
				Name: strings.TrimSpace(strings.TrimPrefix(raw, "Rule:")),
				Line: line,
				Tags: pendingTags,
			}
			pendingTags = nil
			ruleBg = nil
			stepTarget = nil
			inDesc = false
		case strings.HasPrefix(raw, "Background:"):
			if curRule != nil {
				ruleBg = nil
				stepTarget = &ruleBg
			} else {
				featureBg = nil
				stepTarget = &featureBg
			}
			inDesc = false
		case strings.HasPrefix(raw, "Scenario:"), strings.HasPrefix(raw, "Scenario Outline:"):
			name := raw
			if strings.HasPrefix(raw, "Scenario Outline:") {
				name = strings.TrimSpace(strings.TrimPrefix(raw, "Scenario Outline:"))
			} else {
				name = strings.TrimSpace(strings.TrimPrefix(raw, "Scenario:"))
			}
			// Prepend the applicable backgrounds (feature, then rule) so the scenario
			// executes exactly the steps it did before the Background was extracted.
			base := append([]GherkinStep{}, featureBg...)
			if curRule != nil {
				base = append(base, ruleBg...)
			}
			s := &GherkinScenario{Name: name, Line: line, Tags: pendingTags, Rule: curRule, Steps: base}
			pendingTags = nil
			scenarios = append(scenarios, s)
			stepTarget = &s.Steps
			inDesc = false
		default:
			if kw, text, ok := splitStep(raw); ok {
				if stepTarget != nil {
					*stepTarget = append(*stepTarget, GherkinStep{Keyword: kw, Text: text, Line: line})
				}
				inDesc = false
			} else if inDesc {
				descLines = append(descLines, raw)
			}
		}
	}
	feat.Description = strings.TrimSpace(strings.Join(descLines, "\n"))
	for _, s := range scenarios {
		feat.Scenarios = append(feat.Scenarios, *s)
	}
	return feat, sc.Err()
}

// parseTags splits a `@a @b` tag line into individual tag tokens.
func parseTags(raw string) []string {
	var out []string
	for _, f := range strings.Fields(raw) {
		if strings.HasPrefix(f, "@") {
			out = append(out, f)
		}
	}
	return out
}

// splitStep recognises a step line, returning its keyword (trimmed, no trailing
// space) and the step text the registry is keyed on. A line that is not a step
// (ok=false) is ignored by the caller.
func splitStep(raw string) (keyword, text string, ok bool) {
	for _, kw := range stepKeywords {
		if strings.HasPrefix(raw, kw) {
			return strings.TrimSpace(kw), strings.TrimSpace(strings.TrimPrefix(raw, kw)), true
		}
	}
	return "", "", false
}
