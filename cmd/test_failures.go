package cmd

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/Dawil/draiver/internal/cucumber"
)

// rungFailure is the error a required rung's failure carries out of climb (drv-063):
// which rung went red and, when draiver could tell, which tests/scenarios failed in
// it — so a detached fail result can record them and a refused review can name them.
// It unwraps to the underlying error, so an env fault's *exitError (and its exit
// code) is still found by errors.As.
type rungFailure struct {
	Rung  string
	Tests []string
	// run is true when the rung's own `run` command failed (a code fault), as opposed
	// to its environment (up/healthcheck), whose error message is kept verbatim.
	run bool
	err error
}

func (e *rungFailure) Error() string {
	if !e.run {
		return e.err.Error()
	}
	msg := fmt.Sprintf("rung %q not green: %v", e.Rung, e.err)
	if len(e.Tests) > 0 {
		msg += " — failing: " + strings.Join(e.Tests, ", ")
	}
	return msg
}

func (e *rungFailure) Unwrap() error { return e.err }

// maxFailedTests caps how many failing names a result records: enough to point at
// the fault, without copying a whole broken suite into the log.
const maxFailedTests = 20

var (
	goTestFailRe = regexp.MustCompile(`^\s*--- FAIL: (\S+)`)
	goPkgFailRe  = regexp.MustCompile(`^FAIL\s+(\S+)(\s+\[[^\]]+\])?`)
)

// failScanner is an io.Writer tee'd onto a rung's `run` output that recognises the
// failures a test runner prints: Go's `--- FAIL: TestX` lines and, when no test is
// named (e.g. a build failure), its `FAIL <pkg> [build failed]` lines. It is
// runner-agnostic in effect — output it does not recognise simply yields no names.
type failScanner struct {
	mu    sync.Mutex
	part  []byte
	tests []string
	pkgs  []string
	seen  map[string]bool
}

func (f *failScanner) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.part = append(f.part, p...)
	for {
		i := bytes.IndexByte(f.part, '\n')
		if i < 0 {
			break
		}
		f.line(string(f.part[:i]))
		f.part = f.part[i+1:]
	}
	return len(p), nil
}

func (f *failScanner) line(l string) {
	if f.seen == nil {
		f.seen = map[string]bool{}
	}
	if m := goTestFailRe.FindStringSubmatch(l); m != nil {
		if !f.seen[m[1]] {
			f.seen[m[1]] = true
			f.tests = append(f.tests, m[1])
		}
		return
	}
	if m := goPkgFailRe.FindStringSubmatch(l); m != nil {
		name := m[1] + m[2]
		if !f.seen[name] {
			f.seen[name] = true
			f.pkgs = append(f.pkgs, name)
		}
	}
}

// failures returns the recognised failing tests (or, failing that, packages).
func (f *failScanner) failures() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.part) > 0 {
		f.line(string(f.part))
		f.part = nil
	}
	if len(f.tests) > 0 {
		return capNames(f.tests)
	}
	return capNames(f.pkgs)
}

// failedScenarios names every failed scenario in a BDD rung's cucumber report as
// "<feature>: <scenario>".
func failedScenarios(rep cucumber.Report) []string {
	var out []string
	for _, f := range rep {
		for _, el := range f.Elements {
			for _, st := range el.Steps {
				if s := st.Result.Status; s == "failed" || s == "undefined" {
					out = append(out, f.Name+": "+el.Name)
					break
				}
			}
		}
	}
	return capNames(out)
}

func capNames(names []string) []string {
	if len(names) <= maxFailedTests {
		return names
	}
	return append(append([]string{}, names[:maxFailedTests]...), fmt.Sprintf("… and %d more", len(names)-maxFailedTests))
}
