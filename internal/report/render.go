package report

import (
	"bytes"
	"embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"strings"
	"time"
)

//go:embed templates/report.html.tmpl
var tmplFS embed.FS

//go:embed assets/report.css assets/report.js
var assetsFS embed.FS

// Options configures a render. The zero value renders a fully self-contained,
// offline-openable file with every embedding inlined as a data: URI.
type Options struct {
	// Title is the document <title> and page heading. Defaults to "BDD report".
	Title string

	// Resolve, when non-nil, is consulted for every embedding. If it returns a
	// non-empty URL, that embedding is referenced by the URL (<img src=URL> or a
	// download link) instead of being inlined as a base64 data: URI. This is how
	// the webui serves heavy screenshots as artefacts by reference rather than
	// inlining megabytes into the attempt page (drv-017 store-growth note). Leave
	// nil for the standalone download, which must inline everything to open offline.
	Resolve func(EmbeddingRef) string
}

// EmbeddingRef identifies one embedding within a report so a Resolve hook can map
// it to a servable artefact URL. The indices are stable addresses into the parsed
// document: feature → element → (phase, step) → embedding.
type EmbeddingRef struct {
	Feature int
	Element int
	// Phase is where the owning step/hook lives: "before", "step", or "after".
	Phase string
	Step  int
	Index int

	MimeType string
	Name     string
}

// renderModel is the top-level template view-model.
type renderModel struct {
	Title       string
	Summary     Summary
	ScenarioBar []segment
	Legend      []segment
	Stats       []stat
	Verdict     string // "ok" | "bad" | ""
	VerdictText string
	Features    []featureVM
	Empty       bool
	CSS         template.CSS
	JS          template.JS
}

type stat struct {
	N int
	K string
}

type segment struct {
	Status  Status
	Label   string
	Count   int
	Percent float64
	Class   string // seg-passed etc. for the bar / status-passed for dots
}

type featureVM struct {
	Keyword     string
	Name        string
	URI         string
	Description string
	Tags        []string
	Status      Status
	StatusClass string
	Duration    string
	Scenarios   []scenarioVM
}

type scenarioVM struct {
	Keyword     string
	Name        string
	Description string
	Tags        []string
	Status      Status
	StatusClass string
	Duration    string
	Steps       []stepVM
}

type stepVM struct {
	Keyword      string
	Name         string
	StatusClass  string
	StatusText   string
	Duration     string
	Location     string
	ErrorMessage string
	Output       []string
	Rows         [][]string
	DocString    string
	Embeds       []embedVM
	IsHook       bool
	HookKind     string // "Before" | "After"
}

type embedVM struct {
	Name     string
	MimeType string
	IsImage  bool
	IsText   bool
	Src      template.URL // data: URI (inlined) or external URL (by reference)
	Text     string       // decoded text, only for inlined text/* embeddings
	Download bool         // render a download link rather than inline preview
}

// Render turns a parsed report into a single HTML document. With Options.Resolve
// nil the result is fully self-contained — inlined CSS/JS and base64 data: URIs
// for every screenshot — so it opens offline from the filesystem.
func Render(r *Report, opts Options) ([]byte, error) {
	css, err := assetsFS.ReadFile("assets/report.css")
	if err != nil {
		return nil, err
	}
	js, err := assetsFS.ReadFile("assets/report.js")
	if err != nil {
		return nil, err
	}

	title := opts.Title
	if title == "" {
		title = "BDD report"
	}
	sum := r.Summarize()

	model := renderModel{
		Title:   title,
		Summary: sum,
		Stats: []stat{
			{sum.Features, "features"},
			{sum.Scenarios, "scenarios"},
			{sum.Steps, "steps"},
		},
		CSS:   template.CSS(css),
		JS:    template.JS(js),
		Empty: sum.Features == 0,
	}

	// Scenario status bar + legend, worst-first.
	for _, st := range orderedStatuses(sum.ScenariosByStatus) {
		n := sum.ScenariosByStatus[st]
		pct := 0.0
		if sum.Scenarios > 0 {
			pct = float64(n) / float64(sum.Scenarios) * 100
		}
		model.ScenarioBar = append(model.ScenarioBar, segment{
			Status: st, Label: label(st), Count: n, Percent: pct, Class: "seg-" + classOf(st),
		})
		model.Legend = append(model.Legend, segment{
			Status: st, Label: label(st), Count: n, Class: "status-" + classOf(st),
		})
	}

	if sum.Scenarios > 0 && sum.ScenariosByStatus[StatusPassed] == sum.Scenarios {
		model.Verdict, model.VerdictText = "ok", "All scenarios passed"
	} else if sum.ScenariosByStatus[StatusFailed] > 0 {
		model.Verdict, model.VerdictText = "bad", fmt.Sprintf("%d failed", sum.ScenariosByStatus[StatusFailed])
	}

	for fi, f := range r.Features {
		fvm := featureVM{
			Keyword:     orDefault(f.Keyword, "Feature"),
			Name:        f.Name,
			URI:         f.URI,
			Description: strings.TrimSpace(f.Description),
			Tags:        tagNames(f.Tags),
		}
		var fdur time.Duration
		worst := Status("")
		for ei, el := range f.Elements {
			svm := scenarioVM{
				Keyword:     orDefault(el.Keyword, "Scenario"),
				Name:        el.Name,
				Description: strings.TrimSpace(el.Description),
				Tags:        tagNames(el.Tags),
				Status:      el.Status(),
				StatusClass: classOf(el.Status()),
				Duration:    formatDuration(el.Duration()),
			}
			fdur += el.Duration()
			if el.Status().severity() > worst.severity() {
				worst = el.Status()
			}
			for hi, h := range el.Before {
				svm.Steps = append(svm.Steps, hookStep(fi, ei, "before", hi, h, opts))
			}
			for si, s := range el.Steps {
				svm.Steps = append(svm.Steps, renderStep(fi, ei, "step", si, s, opts))
			}
			for hi, h := range el.After {
				svm.Steps = append(svm.Steps, hookStep(fi, ei, "after", hi, h, opts))
			}
			fvm.Scenarios = append(fvm.Scenarios, svm)
		}
		if worst == "" {
			worst = StatusSkipped
		}
		fvm.Status = worst
		fvm.StatusClass = classOf(worst)
		fvm.Duration = formatDuration(fdur)
		model.Features = append(model.Features, fvm)
	}

	t, err := template.New("report.html.tmpl").Funcs(template.FuncMap{
		"durationText": func() string { return formatDuration(sum.Duration) },
	}).ParseFS(tmplFS, "templates/report.html.tmpl")
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "report.html.tmpl", model); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func renderStep(fi, ei int, phase string, si int, s Step, opts Options) stepVM {
	vm := stepVM{
		Keyword:      strings.TrimSpace(s.Keyword),
		Name:         s.Name,
		StatusClass:  classOf(s.Result.Status),
		StatusText:   label(s.Result.Status),
		Duration:     formatDuration(time.Duration(s.Result.Duration)),
		Location:     s.Match.Location,
		ErrorMessage: s.Result.ErrorMessage,
		Output:       s.Output,
	}
	for _, row := range s.Rows {
		vm.Rows = append(vm.Rows, row.Cells)
	}
	if s.DocString != nil {
		vm.DocString = s.DocString.Value
	}
	vm.Embeds = renderEmbeds(fi, ei, phase, si, s.Embeddings, opts)
	return vm
}

func hookStep(fi, ei int, phase string, hi int, h Hook, opts Options) stepVM {
	kind := "Before"
	if phase == "after" {
		kind = "After"
	}
	vm := stepVM{
		Keyword:      kind + " hook",
		Name:         shortLocation(h.Match.Location),
		StatusClass:  classOf(h.Result.Status),
		StatusText:   label(h.Result.Status),
		Duration:     formatDuration(time.Duration(h.Result.Duration)),
		Location:     h.Match.Location,
		ErrorMessage: h.Result.ErrorMessage,
		Output:       h.Output,
		IsHook:       true,
		HookKind:     kind,
	}
	vm.Embeds = renderEmbeds(fi, ei, phase, hi, h.Embeddings, opts)
	return vm
}

func renderEmbeds(fi, ei int, phase string, si int, embs []Embedding, opts Options) []embedVM {
	var out []embedVM
	for idx, e := range embs {
		ev := embedVM{Name: embedName(e, idx), MimeType: e.MimeType}
		isImg := strings.HasPrefix(e.MimeType, "image/")
		isText := strings.HasPrefix(e.MimeType, "text/") ||
			e.MimeType == "application/json" || e.MimeType == "application/xml"

		var url string
		if opts.Resolve != nil {
			url = opts.Resolve(EmbeddingRef{
				Feature: fi, Element: ei, Phase: phase, Step: si, Index: idx,
				MimeType: e.MimeType, Name: e.Name,
			})
		}
		switch {
		case url != "":
			// By-reference: point at a served artefact. Works for any type.
			ev.Src = template.URL(url)
			ev.IsImage = isImg
			ev.Download = !isImg
		case isImg:
			ev.IsImage = true
			ev.Src = template.URL("data:" + e.MimeType + ";base64," + e.Data)
		case isText:
			ev.IsText = true
			ev.Text = decodeText(e.Data)
		default:
			// Unknown binary type, inlined: offer a download link via data: URI.
			ev.Download = true
			ev.Src = template.URL("data:" + e.MimeType + ";base64," + e.Data)
		}
		out = append(out, ev)
	}
	return out
}

func decodeText(b64 string) string {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return b64 // fall back to raw if it wasn't valid base64
	}
	return string(raw)
}

func embedName(e Embedding, idx int) string {
	if e.Name != "" {
		return e.Name
	}
	return fmt.Sprintf("attachment %d (%s)", idx+1, e.MimeType)
}

func shortLocation(loc string) string {
	if loc == "" {
		return "hook"
	}
	return loc
}

func classOf(s Status) string {
	switch s {
	case StatusPassed, StatusFailed, StatusSkipped, StatusPending, StatusUndefined, StatusAmbiguous:
		return string(s)
	default:
		return "unknown"
	}
}

func label(s Status) string {
	if s == "" {
		return "unknown"
	}
	return string(s)
}

func tagNames(tags []Tag) []string {
	if len(tags) == 0 {
		return nil
	}
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		out = append(out, t.Name)
	}
	return out
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return strings.TrimSpace(s)
}

// formatDuration renders a cucumber nanosecond duration compactly.
func formatDuration(d time.Duration) string {
	switch {
	case d <= 0:
		return "0ms"
	case d < time.Microsecond:
		return fmt.Sprintf("%dns", d.Nanoseconds())
	case d < time.Millisecond:
		return fmt.Sprintf("%dµs", d.Microseconds())
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.2fs", d.Seconds())
	default:
		m := int(d / time.Minute)
		s := d - time.Duration(m)*time.Minute
		return fmt.Sprintf("%dm%.1fs", m, s.Seconds())
	}
}
