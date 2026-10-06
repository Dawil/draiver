package acceptance

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// This file is a deliberately minimal Gherkin reader — enough to drive drv-019's
// own acceptance features, not a full parser. It understands Feature/Scenario and
// the step keywords (Given/When/Then/And/But); tags, comments, and free-text
// description lines are skipped. draiver consumes cucumber-JSON runner-agnostically,
// so the runner only needs to turn these features into the steps it executes and
// the JSON it emits — not to implement the whole Gherkin grammar.

// stepKeywords are the line prefixes that introduce a step. "And"/"But" continue
// the previous step's intent; we keep the literal keyword for display and match on
// the step text, so a registry entry is shared regardless of the leading keyword.
var stepKeywords = []string{"Given ", "When ", "Then ", "And ", "But "}

// GherkinStep is one parsed step: its leading keyword and the step text the
// registry is keyed on.
type GherkinStep struct {
	Keyword string
	Text    string
	Line    int
}

// GherkinScenario is one scenario and its ordered steps.
type GherkinScenario struct {
	Name  string
	Line  int
	Steps []GherkinStep
}

// GherkinFeature is one .feature file: its name, source URI, and scenarios.
type GherkinFeature struct {
	Name      string
	URI       string
	Scenarios []GherkinScenario
}

// ParseFeaturesDir reads every *.feature under dir (sorted by path for a stable
// run order) and returns the parsed features.
func ParseFeaturesDir(dir string) ([]GherkinFeature, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.feature"))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	var features []GherkinFeature
	for _, path := range matches {
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
	var cur *GherkinScenario
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	line := 0
	for sc.Scan() {
		line++
		raw := strings.TrimSpace(sc.Text())
		switch {
		case raw == "" || strings.HasPrefix(raw, "#") || strings.HasPrefix(raw, "@"):
			continue
		case strings.HasPrefix(raw, "Feature:"):
			feat.Name = strings.TrimSpace(strings.TrimPrefix(raw, "Feature:"))
		case strings.HasPrefix(raw, "Scenario:"):
			feat.Scenarios = append(feat.Scenarios, GherkinScenario{
				Name: strings.TrimSpace(strings.TrimPrefix(raw, "Scenario:")),
				Line: line,
			})
			cur = &feat.Scenarios[len(feat.Scenarios)-1]
		default:
			if cur == nil {
				continue // free-text description under Feature — ignored.
			}
			if kw, text, ok := splitStep(raw); ok {
				cur.Steps = append(cur.Steps, GherkinStep{Keyword: kw, Text: text, Line: line})
			}
		}
	}
	return feat, sc.Err()
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
