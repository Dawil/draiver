package acceptance

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/webshot"
)

// withFocusSteps registers the two fixture steps the testdata/focus features use for
// the duration of a test (they are not part of the shipped registry).
func withFocusSteps(t *testing.T) {
	t.Helper()
	steps := map[string]StepFunc{
		"a drv-063 focus fixture step": func(w *World, sr *StepRun) error {
			attachText(sr, "focus", "ran")
			return nil
		},
		"a drv-063 focus fixture step without an artefact": func(w *World, sr *StepRun) error { return nil },
	}
	for k, v := range steps {
		registry[k] = v
	}
	t.Cleanup(func() {
		for k := range steps {
			delete(registry, k)
		}
	})
}

func scenarioNames(r Result) []string {
	var out []string
	for _, f := range r.Report {
		for _, el := range f.Elements {
			out = append(out, el.Name)
		}
	}
	sort.Strings(out)
	return out
}

func TestRunFocusedSelectsByTagNameAndDir(t *testing.T) {
	withFocusSteps(t)
	cases := []struct {
		name string
		dir  string
		opts RunOptions
		want []string
	}{
		{"everything", "testdata/focus", RunOptions{}, []string{"Alpha plain", "Alpha ruled", "Alpha shown in the UI", "Beta shown in the UI", "Beta silent"}},
		{"feature tag is inherited", "testdata/focus", RunOptions{Tags: []string{"@drv-900"}}, []string{"Alpha plain", "Alpha ruled", "Alpha shown in the UI"}},
		{"scenario tag inside a rule", "testdata/focus", RunOptions{Tags: []string{"drv-901"}}, []string{"Alpha ruled"}},
		{"tags are OR-ed", "testdata/focus", RunOptions{Tags: ParseTags("@drv-901,@drv-950")}, []string{"Alpha ruled", "Beta shown in the UI", "Beta silent"}},
		{"scenario tag across features", "testdata/focus", RunOptions{Tags: []string{"@ui"}}, []string{"Alpha shown in the UI", "Beta shown in the UI"}},
		{"name filter is case-insensitive", "testdata/focus", RunOptions{Name: "SHOWN"}, []string{"Alpha shown in the UI", "Beta shown in the UI"}},
		{"tag and name combine", "testdata/focus", RunOptions{Tags: []string{"@ui"}, Name: "beta"}, []string{"Beta shown in the UI"}},
		{"dir narrows to one folder", "testdata/focus/beta", RunOptions{}, []string{"Beta shown in the UI", "Beta silent"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := RunFocused(tc.dir, t.TempDir(), tc.opts)
			if err != nil {
				t.Fatalf("RunFocused: %v", err)
			}
			if got := scenarioNames(res); strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("ran %q, want %q", got, tc.want)
			}
			if res.ScenariosSelected != len(tc.want) {
				t.Errorf("ScenariosSelected = %d, want %d", res.ScenariosSelected, len(tc.want))
			}
		})
	}
}

func TestRunFocusedMatchingNothingIsAnError(t *testing.T) {
	withFocusSteps(t)
	_, err := RunFocused("testdata/focus", t.TempDir(), RunOptions{Tags: []string{"@drv-nope"}})
	if err == nil || !strings.Contains(err.Error(), "matches the focus") {
		t.Fatalf("err = %v, want a no-match error", err)
	}
}

func TestParseTags(t *testing.T) {
	got := ParseTags("@drv-046, ui ,,@x")
	if strings.Join(got, ",") != "@drv-046,@ui,@x" {
		t.Fatalf("ParseTags = %q", got)
	}
}

// The runtime half of the artefact rule: a scenario that passed without attaching
// anything is reported; one that attached is not.
func TestMissingArtefactsFlagsPassingScenarioWithoutEmbedding(t *testing.T) {
	withFocusSteps(t)
	res, err := RunFocused("testdata/focus/beta", t.TempDir(), RunOptions{})
	if err != nil {
		t.Fatalf("RunFocused: %v", err)
	}
	got := MissingArtefacts(res.Report)
	if len(got) != 1 || got[0] != "Beta focus fixture: Beta silent" {
		t.Fatalf("MissingArtefacts = %q", got)
	}
}

// stubWebshot swaps capture's browser seams for the duration of a test.
func stubWebshot(t *testing.T, available bool, capture func(n int) ([]byte, error)) *int {
	t.Helper()
	calls := 0
	oa, oc := webshotAvailable, webshotCapture
	webshotAvailable = func() bool { return available }
	webshotCapture = func(context.Context, string, webshot.Options) ([]byte, error) {
		calls++
		return capture(calls)
	}
	t.Cleanup(func() { webshotAvailable, webshotCapture = oa, oc })
	return &calls
}

func htmlServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<main data-testid="page">rendered</main>`)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestCaptureRetriesOnceThenSucceeds(t *testing.T) {
	calls := stubWebshot(t, true, func(n int) ([]byte, error) {
		if n == 1 {
			return nil, errors.New("flaky")
		}
		return []byte("PNG"), nil
	})
	sr := &StepRun{}
	if err := capture(sr, htmlServer(t), "shot", webshot.Options{}); err != nil {
		t.Fatalf("capture: %v", err)
	}
	if *calls != 2 || len(sr.Embeddings) != 1 || sr.Embeddings[0].MimeType != "image/png" {
		t.Fatalf("calls=%d embeddings=%+v, want a PNG on the second try", *calls, sr.Embeddings)
	}
}

func TestCaptureFallsBackToHTMLAfterTwoFailures(t *testing.T) {
	calls := stubWebshot(t, true, func(int) ([]byte, error) { return nil, errors.New("chromium crashed") })
	sr := &StepRun{}
	if err := capture(sr, htmlServer(t), "shot", webshot.Options{}); err != nil {
		t.Fatalf("capture: %v", err)
	}
	if *calls != captureAttempts {
		t.Errorf("capture tried %d times, want %d", *calls, captureAttempts)
	}
	assertHTMLFallback(t, sr, "chromium crashed")
}

func TestCaptureWithoutBrowserAttachesHTML(t *testing.T) {
	calls := stubWebshot(t, false, func(int) ([]byte, error) { return []byte("PNG"), nil })
	sr := &StepRun{}
	if err := capture(sr, htmlServer(t), "shot", webshot.Options{}); err != nil {
		t.Fatalf("capture: %v", err)
	}
	if *calls != 0 {
		t.Errorf("capture called webshot %d times with no browser", *calls)
	}
	assertHTMLFallback(t, sr, "no playwright browser")
}

func TestCaptureErrorsWhenHTMLFallbackFails(t *testing.T) {
	stubWebshot(t, false, nil)
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	sr := &StepRun{}
	if err := capture(sr, srv.URL, "shot", webshot.Options{}); err == nil || len(sr.Embeddings) != 0 {
		t.Fatalf("err=%v embeddings=%d, want an error and nothing attached", err, len(sr.Embeddings))
	}
}

func assertHTMLFallback(t *testing.T, sr *StepRun, reason string) {
	t.Helper()
	if len(sr.Embeddings) != 1 || sr.Embeddings[0].MimeType != "text/plain" {
		t.Fatalf("embeddings = %+v, want one text/plain HTML fallback", sr.Embeddings)
	}
	raw, _ := base64.StdEncoding.DecodeString(sr.Embeddings[0].Data)
	if !strings.Contains(string(raw), `data-testid="page"`) || !strings.Contains(string(raw), reason) {
		t.Fatalf("fallback body = %q, want the rendered HTML and the reason %q", raw, reason)
	}
}
