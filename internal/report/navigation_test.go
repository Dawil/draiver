package report

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// navSample is a known cucumber-JSON run exercising drv-023 navigation:
//   - two features in different folders (features/auth, features/glossary) so
//     folder grouping has something to derive;
//   - @smoke carried by scenarios in BOTH features (OR tag selection across
//     features) and a feature-level @auth inherited by its scenarios;
//   - a feature WITH Gherkin Rule blocks (glossary: a "Rule: Alphabetical" group
//     plus one rule-less scenario) and one WITHOUT (login: flat);
//   - URIs whose folder segment supports a folder-prefix match ("glossary") and a
//     non-substring subsequence match ("glorder" → glossary/ordered).
var navSample = `[
  {
    "uri": "features/auth/login.feature",
    "keyword": "Feature", "name": "Login",
    "tags": [{"name":"@auth","line":1}],
    "elements": [
      {"keyword":"Scenario","name":"Happy path","type":"scenario","tags":[{"name":"@smoke","line":4}],
       "steps":[{"keyword":"Given ","name":"a registered user","result":{"status":"passed","duration":1000000}}]},
      {"keyword":"Scenario","name":"Bad password","type":"scenario","tags":[{"name":"@edge","line":10}],
       "steps":[{"keyword":"When ","name":"wrong pw","result":{"status":"failed","duration":2000000,"error_message":"got 401"}}]}
    ]
  },
  {
    "uri": "features/glossary/ordered.feature",
    "keyword": "Feature", "name": "Glossary ordering",
    "elements": [
      {"keyword":"Scenario","name":"sorts ascending","type":"scenario","tags":[{"name":"@smoke","line":4}],
       "rule":{"keyword":"Rule","name":"Alphabetical","id":"glossary;alphabetical","line":3},
       "steps":[{"keyword":"Then ","name":"ascending order","result":{"status":"passed","duration":1000000}}]},
      {"keyword":"Scenario","name":"sorts descending","type":"scenario",
       "rule":{"keyword":"Rule","name":"Alphabetical","id":"glossary;alphabetical","line":3},
       "steps":[{"keyword":"Then ","name":"descending order","result":{"status":"passed","duration":1000000}}]},
      {"keyword":"Scenario","name":"ungrouped term","type":"scenario","tags":[{"name":"@edge","line":20}],
       "steps":[{"keyword":"Then ","name":"a term","result":{"status":"passed","duration":1000000}}]}
    ]
  }
]`

// subseqMatch mirrors the in-order (fuzzy, not substring) matcher the inlined JS
// runs over each scenario's data-key, so the Go test can prove the key the
// renderer emits actually supports the folder-prefix and non-substring hits.
func subseqMatch(needle, haystack string) bool {
	i := 0
	for j := 0; j < len(haystack) && i < len(needle); j++ {
		if haystack[j] == needle[i] {
			i++
		}
	}
	return i == len(needle)
}

var dataKeyRe = regexp.MustCompile(`data-key="([^"]*)"`)

func TestNavFolderGroupingAndFacets(t *testing.T) {
	r := mustParse(t, navSample)
	doc := string(mustRender(t, r))

	if !strings.Contains(doc, `data-groupby="folder"`) {
		t.Error("tree should default to folder grouping")
	}
	// Capability sections derived from the feature URIs.
	for _, want := range []string{
		`<span class="title">features/auth</span>`,
		`<span class="title">features/glossary</span>`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("missing folder capability section %q", want)
		}
	}
	// Facet bar lists every effective tag with a count. @auth is feature-level on
	// login, so it is inherited by both login scenarios (count 2); @smoke spans
	// both features (Happy path + sorts ascending); @edge spans both (Bad password
	// + ungrouped term).
	for tag, count := range map[string]int{"@auth": 2, "@smoke": 2, "@edge": 2} {
		if !strings.Contains(doc, `data-tag="`+tag+`"`) {
			t.Errorf("facet bar missing tag %q", tag)
		}
		frag := `data-tag="` + tag + `" aria-pressed="false">` + tag + ` <span class="facet-count">` + strconv.Itoa(count) + `</span>`
		if !strings.Contains(doc, frag) {
			t.Errorf("facet for %q missing or wrong count; want fragment %q", tag, frag)
		}
	}
}

func TestNavRuleBlocks(t *testing.T) {
	r := mustParse(t, navSample)
	doc := string(mustRender(t, r))

	// Exactly one Rule sub-group — glossary's; login emits none and falls back to
	// the flat feature→scenario tree (no regression).
	if n := strings.Count(doc, `<details class="rule"`); n != 1 {
		t.Errorf("rule sub-groups = %d, want 1 (glossary only)", n)
	}
	if !strings.Contains(doc, `<span class="title">Rule: Alphabetical</span>`) {
		t.Error("missing labelled Rule header")
	}
	// The rule rolls up passed (both its scenarios passed).
	ruleStart := strings.Index(doc, `<details class="rule"`)
	ruleSummaryEnd := strings.Index(doc[ruleStart:], "</summary>") + ruleStart
	if !strings.Contains(doc[ruleStart:ruleSummaryEnd], "status-passed") {
		t.Error("rule roll-up dot is not status-passed")
	}
	// The two rule scenarios live inside the rule body; the rule-less "ungrouped
	// term" renders flat, outside the rule's close.
	ruleEnd := strings.Index(doc, "ungrouped term")
	rbody := doc[ruleStart:ruleEnd]
	for _, want := range []string{"sorts ascending", "sorts descending"} {
		if !strings.Contains(rbody, want) {
			t.Errorf("rule scenario %q not grouped under the Rule", want)
		}
	}
}

func TestNavEffectiveTagsOnScenarios(t *testing.T) {
	r := mustParse(t, navSample)
	doc := string(mustRender(t, r))

	// Effective (inherited) tags land in data-tags so the OR filter sees a
	// feature-level @auth on each login scenario.
	wants := []string{
		`data-tags="@auth @smoke"`, // login Happy path
		`data-tags="@auth @edge"`,  // login Bad password
	}
	for _, w := range wants {
		if !strings.Contains(doc, w) {
			t.Errorf("missing effective-tag attribute %q", w)
		}
	}
	// Chips still render where authored: the feature's own @auth chip, not the
	// scenario's.
	if !strings.Contains(doc, `<span class="tag">@auth</span>`) {
		t.Error("feature-level @auth chip not rendered")
	}
}

func TestNavSubsequenceKeySupportsFolderAndFuzzyHits(t *testing.T) {
	r := mustParse(t, navSample)
	doc := string(mustRender(t, r))

	// Pull the composite key of the "sorts descending" scenario.
	var key string
	for _, m := range dataKeyRe.FindAllStringSubmatch(doc, -1) {
		if strings.Contains(m[1], "sorts descending") {
			key = m[1]
			break
		}
	}
	if key == "" {
		t.Fatal("no data-key found for the descending scenario")
	}
	// The key is prefixed with the folder path / URI, lowercased.
	if !strings.HasPrefix(key, "features/glossary/ordered.feature") {
		t.Errorf("key not prefixed with the feature URI: %q", key)
	}
	if key != strings.ToLower(key) {
		t.Errorf("key is not lowercased: %q", key)
	}
	// Folder-prefix hit, non-substring fuzzy hit, and a clean miss.
	if !subseqMatch("glossary", key) {
		t.Error("folder-prefix subsequence 'glossary' should match")
	}
	if !subseqMatch("glorder", key) {
		t.Errorf("fuzzy subsequence 'glorder' should match %q", key)
	}
	if subseqMatch("zqxj", key) {
		t.Error("'zqxj' should not match")
	}
}

// TestNavRendersInBothPaths asserts the navigation controls ship in BOTH delivery
// renders: the self-contained standalone (Resolve nil, inlined) and the
// webui-embedded by-reference render (Resolve set) — the two ways Render is driven.
func TestNavRendersInBothPaths(t *testing.T) {
	r := mustParse(t, navSample)
	paths := map[string]Options{
		"standalone":  {Title: "nav"},
		"webui-embed": {Title: "nav", Resolve: func(EmbeddingRef) string { return "/asset" }},
	}
	for name, opts := range paths {
		out, err := Render(r, opts)
		if err != nil {
			t.Fatalf("%s: Render: %v", name, err)
		}
		doc := string(out)
		for _, want := range []string{
			`class="facets"`,        // tag facet bar
			`data-act="groupby"`,    // group-by control
			`data-act="filter"`,     // subsequence filter box
			`<details class="rule"`, // Rule sub-group
			`data-groupby="folder"`, // default folder grouping
			`data-key="`,            // per-scenario subsequence key
		} {
			if !strings.Contains(doc, want) {
				t.Errorf("%s render missing navigation markup %q", name, want)
			}
		}
	}
}

func mustRender(t *testing.T, r *Report) []byte {
	t.Helper()
	out, err := Render(r, Options{Title: "nav"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	return out
}
