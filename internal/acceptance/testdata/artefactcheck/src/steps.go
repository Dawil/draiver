// Fixture for TestArtefactCheckFlagsScenarioWithoutAttachingStep: a miniature step
// package the static artefact analysis reads. It lives under testdata, so the Go
// toolchain never compiles it.
package fixture

type StepRun struct{ Embeddings []string }
type World struct{}
type StepFunc func(w *World, sr *StepRun) error

var registry = map[string]StepFunc{
	"a seeded fixture":        givenSeeded,
	"the fixture is asserted": thenAsserted,
	"the fixture page is shot": thenShot,
}

func init() {
	for text, fn := range map[string]StepFunc{
		"the fixture output is attached": func(w *World, sr *StepRun) error {
			sr.Embeddings = append(sr.Embeddings, "out")
			return nil
		},
		"the fixture output is shot by helper": withHelper,
	} {
		registry[text] = fn
	}
}

func givenSeeded(w *World, sr *StepRun) error  { return nil }
func thenAsserted(w *World, sr *StepRun) error { return check() }
func check() error                             { return nil }

// thenShot attaches through a two-deep helper chain: thenShot → shoot → capture.
func thenShot(w *World, sr *StepRun) error { return shoot(sr) }
func shoot(sr *StepRun) error              { return capture(sr) }
func capture(sr *StepRun) error {
	sr.Embeddings = append(sr.Embeddings, "png")
	return nil
}

// withHelper passes an attaching function as a value rather than calling it.
func withHelper(w *World, sr *StepRun) error { return run(sr, capture) }
func run(sr *StepRun, fn func(*StepRun) error) error { return fn(sr) }
