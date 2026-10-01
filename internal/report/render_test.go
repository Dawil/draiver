package report

import (
	"encoding/base64"
	"html"
	"strconv"
	"strings"
	"testing"
	"time"
)

// a real, decodable 1x1 transparent PNG.
const onePxPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="

// sample is a known cucumber-JSON document covering: tags, a background, a passed
// scenario, a failed scenario with an error message, a step with an image
// embedding, a data table, a doc string, and the media.type embedding dialect.
var sample = `[
  {
    "uri": "features/login.feature",
    "id": "login",
    "keyword": "Feature",
    "name": "Login",
    "description": "As a user I can log in",
    "line": 1,
    "tags": [{"name": "@auth", "line": 1}],
    "elements": [
      {
        "id": "login;happy-path",
        "keyword": "Scenario",
        "name": "Happy path",
        "type": "scenario",
        "line": 5,
        "tags": [{"name": "@smoke", "line": 4}],
        "steps": [
          {"keyword": "Given ", "name": "a registered user", "line": 6,
           "match": {"location": "steps/login.rb:3"},
           "result": {"status": "passed", "duration": 1200000},
           "rows": [{"cells": ["email", "pw"]}, {"cells": ["a@b.c", "secret"]}]},
          {"keyword": "When ", "name": "they submit the form", "line": 7,
           "result": {"status": "passed", "duration": 2500000},
           "doc_string": {"content_type": "text/plain", "value": "payload body", "line": 8}}
        ]
      },
      {
        "id": "login;bad-password",
        "keyword": "Scenario",
        "name": "Bad password",
        "type": "scenario",
        "line": 12,
        "steps": [
          {"keyword": "When ", "name": "they submit a bad password", "line": 13,
           "result": {"status": "failed", "duration": 800000, "error_message": "expected 200 got 401"},
           "embeddings": [{"mime_type": "image/png", "data": "` + onePxPNG + `", "name": "failure.png"}]},
          {"keyword": "Then ", "name": "they see an error", "line": 14,
           "result": {"status": "skipped", "duration": 0}}
        ],
        "after": [
          {"match": {"location": "hooks.rb:9"}, "result": {"status": "passed", "duration": 500000},
           "embeddings": [{"media": {"type": "text/plain"}, "data": "` +
	base64.StdEncoding.EncodeToString([]byte("page source dump")) + `", "name": "page.txt"}]}
        ]
      }
    ]
  }
]`

func mustParse(t *testing.T, s string) *Report {
	t.Helper()
	r, err := Parse([]byte(s))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return r
}

func TestParseAndSummarize(t *testing.T) {
	r := mustParse(t, sample)
	sum := r.Summarize()
	if sum.Features != 1 {
		t.Errorf("features = %d, want 1", sum.Features)
	}
	if sum.Scenarios != 2 {
		t.Errorf("scenarios = %d, want 2", sum.Scenarios)
	}
	if got := sum.ScenariosByStatus[StatusPassed]; got != 1 {
		t.Errorf("passed scenarios = %d, want 1", got)
	}
	if got := sum.ScenariosByStatus[StatusFailed]; got != 1 {
		t.Errorf("failed scenarios = %d, want 1", got)
	}
	// 4 steps + 1 after-hook = 5 counted results.
	if sum.Steps != 5 {
		t.Errorf("steps = %d, want 5", sum.Steps)
	}
	if sum.Passed() {
		t.Error("Passed() = true, want false (one scenario failed)")
	}
}

func TestScenarioStatusRollup(t *testing.T) {
	r := mustParse(t, sample)
	got := r.Features[0].Elements[1].Status()
	if got != StatusFailed {
		t.Errorf("bad-password scenario status = %q, want failed", got)
	}
}

func TestEmbeddingDialects(t *testing.T) {
	r := mustParse(t, sample)
	png := r.Features[0].Elements[1].Steps[0].Embeddings[0]
	if png.MimeType != "image/png" || png.Data != onePxPNG {
		t.Errorf("mime_type dialect not parsed: %+v", png)
	}
	txt := r.Features[0].Elements[1].After[0].Embeddings[0]
	if txt.MimeType != "text/plain" {
		t.Errorf("media.type dialect not parsed: mime=%q", txt.MimeType)
	}
}

func TestRenderSelfContainedAndOffline(t *testing.T) {
	r := mustParse(t, sample)
	out, err := Render(r, Options{Title: "Login run"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	doc := string(out)
	// HTML-unescape for the exact data-URI match: the attribute escaper encodes
	// the base64 '+' as &#43;, which a browser decodes back to '+' when parsing
	// src — so the file is genuinely self-contained; the raw bytes just carry the
	// entity form.
	unescaped := html.UnescapeString(doc)

	// The screenshot must be inlined as a base64 data: URI so the file opens offline.
	if !strings.Contains(unescaped, "data:image/png;base64,"+onePxPNG) {
		t.Error("rendered HTML does not inline the PNG as a data: URI")
	}
	// CSS and JS must be inlined, not linked — no external references at all.
	for _, bad := range []string{"<link ", "src=\"http", "src='http", "href=\"http", "href='http", "src=\"/static", ".css\"", ".js\""} {
		if strings.Contains(doc, bad) {
			t.Errorf("rendered HTML is not self-contained: found %q", bad)
		}
	}
	if !strings.Contains(doc, "<style>") || !strings.Contains(doc, "<script>") {
		t.Error("expected inline <style> and <script> blocks")
	}
	// Content sanity.
	for _, want := range []string{"Login run", "Happy path", "Bad password", "expected 200 got 401", "@smoke", "payload body", "page source dump"} {
		if !strings.Contains(doc, want) {
			t.Errorf("rendered HTML missing %q", want)
		}
	}
}

func TestRenderByReferenceDoesNotInline(t *testing.T) {
	r := mustParse(t, sample)
	var refs []EmbeddingRef
	out, err := Render(r, Options{
		Title: "ref",
		Resolve: func(ref EmbeddingRef) string {
			refs = append(refs, ref)
			return "/ticket/T/0001/report/asset?f=" + strconv.Itoa(ref.Feature) + "&i=" + strconv.Itoa(ref.Index)
		},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	doc := html.UnescapeString(string(out))
	if strings.Contains(doc, onePxPNG) {
		t.Error("by-reference render must not inline base64 image data")
	}
	if !strings.Contains(doc, "/ticket/T/0001/report/asset?f=0&i=0") {
		t.Error("by-reference render did not use the resolved URL")
	}
	if len(refs) != 2 { // png on the step + text on the after-hook
		t.Errorf("Resolve called %d times, want 2", len(refs))
	}
	// A referenced image should still be an <img>, referenced text a download link.
	if !strings.Contains(doc, "<img") {
		t.Error("expected an <img> for the referenced screenshot")
	}
}

func TestLargeEmbeddingDoesNotBreak(t *testing.T) {
	// ~3 MB of base64 payload — the standalone-download heavy case.
	big := strings.Repeat("A", 3<<20)
	doc := `[{"uri":"f","keyword":"Feature","name":"big","elements":[
	  {"keyword":"Scenario","name":"s","type":"scenario","steps":[
	    {"keyword":"Given ","name":"a huge screenshot",
	     "result":{"status":"passed","duration":1},
	     "embeddings":[{"mime_type":"image/png","data":"` + big + `"}]}]}]}]`
	r := mustParse(t, doc)
	out, err := Render(r, Options{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(out), big) {
		t.Error("large embedding was dropped from the output")
	}
	if len(out) < 3<<20 {
		t.Errorf("output unexpectedly small (%d bytes)", len(out))
	}
}

func TestParseEmptyAndMalformed(t *testing.T) {
	if r, err := Parse(nil); err != nil || len(r.Features) != 0 {
		t.Errorf("Parse(nil) = %v, %v; want empty report, nil", r, err)
	}
	if r, err := Parse([]byte("   \n ")); err != nil || len(r.Features) != 0 {
		t.Errorf("Parse(blank) = %v, %v; want empty report, nil", r, err)
	}
	if _, err := Parse([]byte("{not json")); err == nil {
		t.Error("Parse(malformed) = nil error, want error")
	}
	// An empty report still renders (no panic, shows the empty state).
	out, err := Render(&Report{}, Options{})
	if err != nil {
		t.Fatalf("Render(empty): %v", err)
	}
	if !strings.Contains(string(out), "No features") {
		t.Error("empty render should show the empty state")
	}
}

func TestFormatDuration(t *testing.T) {
	cases := map[int64]string{
		0:             "0ms",
		500:           "500ns",
		1200:          "1µs",
		2_500_000:     "2ms",
		1_500_000_000: "1.50s",
	}
	for ns, want := range cases {
		if got := formatDuration(time.Duration(ns)); got != want {
			t.Errorf("formatDuration(%dns) = %q, want %q", ns, got, want)
		}
	}
}
