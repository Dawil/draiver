package acceptance

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The browser-free artefact rule (drv-063). Every scenario must attach an artefact
// (a screenshot or a text embedding) — the property the board's report page relies
// on. TestEveryScenarioHasAnArtifact proves it by RUNNING the suite, which needs a
// browser and so only runs under `-tags bdd` / the acceptance rung. This file proves
// it statically, on the unit rung, without executing a single step:
//
//  1. parse this package's Go sources and find every step registration — the keys
//     of each `map[string]StepFunc{…}` literal and each `registry["…"] = fn`;
//  2. mark a function as *attaching* when its body assigns to some `x.Embeddings`
//     (what attachText and capture do), or references a function that attaches —
//     a fixpoint over the package's own call graph, so `shoot` → `capture` →
//     `sr.Embeddings = …` is followed through any number of helpers;
//  3. walk the parsed features and flag every scenario none of whose steps maps to
//     an attaching step definition.
//
// "Can attach" is deliberately an over-approximation (a step that attaches on only
// one branch counts): the check catches the real failure mode — a scenario whose
// steps never attach anything at all — and the acceptance runner still enforces the
// rule at runtime on the full suite.

// stepAttachIndex is the analysis result: for each registered step text, whether its
// definition can attach an artefact, plus any registration whose key could not be
// resolved to a constant string (reported, never silently dropped).
type stepAttachIndex struct {
	attaches   map[string]bool
	unresolved []string
}

// analyseStepSources builds the stepAttachIndex for the non-test Go files in dir.
func analyseStepSources(dir string) (stepAttachIndex, error) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return stepAttachIndex{}, err
	}
	var files []*ast.File
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, n), nil, 0)
		if err != nil {
			return stepAttachIndex{}, err
		}
		files = append(files, f)
	}

	// Top-level functions and methods, keyed by name. A method is keyed by its bare
	// name: a selector call `x.m()` is matched on m, an over-approximation that only
	// ever widens "can attach".
	funcs := map[string][]*ast.FuncDecl{}
	consts := map[string]ast.Expr{}
	for _, f := range files {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Body != nil {
					funcs[d.Name.Name] = append(funcs[d.Name.Name], d)
				}
			case *ast.GenDecl:
				if d.Tok != token.CONST {
					continue
				}
				for _, sp := range d.Specs {
					vs := sp.(*ast.ValueSpec)
					for i, n := range vs.Names {
						if i < len(vs.Values) {
							consts[n.Name] = vs.Values[i]
						}
					}
				}
			}
		}
	}

	// Fixpoint: a function attaches if it assigns to .Embeddings directly or names
	// (calls, or passes as a value) a function that attaches.
	attaching := map[string]bool{}
	for changed := true; changed; {
		changed = false
		for name, decls := range funcs {
			if attaching[name] {
				continue
			}
			for _, d := range decls {
				if nodeAttaches(d.Body, attaching) {
					attaching[name] = true
					changed = true
					break
				}
			}
		}
	}

	// Variables holding a step map (`steps := map[string]StepFunc{…}`) plus the
	// registry itself: an index assignment into one of them is a registration.
	stepVars := map[string]bool{"registry": true}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.AssignStmt:
				for i, lhs := range n.Lhs {
					if id, ok := lhs.(*ast.Ident); ok && i < len(n.Rhs) {
						if cl, ok := n.Rhs[i].(*ast.CompositeLit); ok && isStepMap(cl.Type) {
							stepVars[id.Name] = true
						}
					}
				}
			case *ast.ValueSpec:
				for i, id := range n.Names {
					if i < len(n.Values) {
						if cl, ok := n.Values[i].(*ast.CompositeLit); ok && isStepMap(cl.Type) {
							stepVars[id.Name] = true
						}
					}
				}
			}
			return true
		})
	}

	idx := stepAttachIndex{attaches: map[string]bool{}}
	record := func(text string, val ast.Expr) {
		idx.attaches[text] = idx.attaches[text] || nodeAttaches(val, attaching)
	}
	// indexAssigns records `stepVar[key] = fn` under n, evaluating key with the loop
	// bindings in scope. A key it cannot resolve without a binding is left alone (the
	// `registry[text] = fn` copy loops); TestArtefactAnalysisCoversTheRegistry catches
	// a registration shape that slips through.
	indexAssigns := func(n ast.Node, bind map[string]string) {
		ast.Inspect(n, func(m ast.Node) bool {
			as, ok := m.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for i, lhs := range as.Lhs {
				ix, ok := lhs.(*ast.IndexExpr)
				if !ok || i >= len(as.Rhs) {
					continue
				}
				if id, ok := ix.X.(*ast.Ident); !ok || !stepVars[id.Name] {
					continue
				}
				if text, ok := constString(ix.Index, consts, bind); ok {
					record(text, as.Rhs[i])
				}
			}
			return true
		})
	}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CompositeLit:
				if !isStepMap(n.Type) {
					return true
				}
				for _, el := range n.Elts {
					kv, ok := el.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					text, ok := constString(kv.Key, consts, nil)
					if !ok {
						idx.unresolved = append(idx.unresolved, exprPos(fset, kv.Key))
						continue
					}
					record(text, kv.Value)
				}
			case *ast.RangeStmt:
				// for _, s := range []string{"a", "b"} { steps[`… "`+s+`"`] = f(s) } —
				// a registration per element, the key built from the loop variable.
				// The loop's own body is still walked below (unbound keys are skipped),
				// so a step-map literal inside it is not missed.
				v, vok := n.Value.(*ast.Ident)
				lit, lok := n.X.(*ast.CompositeLit)
				if !vok || !lok {
					return true
				}
				if _, isSlice := lit.Type.(*ast.ArrayType); !isSlice {
					return true
				}
				for _, el := range lit.Elts {
					if s, ok := constString(el, consts, nil); ok {
						indexAssigns(n.Body, map[string]string{v.Name: s})
					}
				}
			case *ast.AssignStmt:
				indexAssigns(n, nil)
			}
			return true
		})
	}
	return idx, nil
}

// nodeAttaches reports whether n (a function body or a registered value) assigns to
// an `.Embeddings` field or names a function already known to attach.
func nodeAttaches(n ast.Node, attaching map[string]bool) bool {
	found := false
	ast.Inspect(n, func(m ast.Node) bool {
		if found {
			return false
		}
		switch m := m.(type) {
		case *ast.AssignStmt:
			for _, lhs := range m.Lhs {
				if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "Embeddings" {
					found = true
				}
			}
		case *ast.Ident:
			if attaching[m.Name] {
				found = true
			}
		}
		return !found
	})
	return found
}

// isStepMap reports whether a composite literal's type is map[string]StepFunc.
func isStepMap(t ast.Expr) bool {
	mt, ok := t.(*ast.MapType)
	if !ok {
		return false
	}
	k, kok := mt.Key.(*ast.Ident)
	v, vok := mt.Value.(*ast.Ident)
	return kok && vok && k.Name == "string" && v.Name == "StepFunc"
}

// constString evaluates a step-text key: a string literal, a `+` of literals, a
// package-level string constant, or a range variable bound in bind.
func constString(e ast.Expr, consts map[string]ast.Expr, bind map[string]string) (string, bool) {
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(e.Value)
		return s, err == nil
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		l, lok := constString(e.X, consts, bind)
		r, rok := constString(e.Y, consts, bind)
		return l + r, lok && rok
	case *ast.ParenExpr:
		return constString(e.X, consts, bind)
	case *ast.Ident:
		if v, ok := bind[e.Name]; ok {
			return v, true
		}
		if v, ok := consts[e.Name]; ok {
			return constString(v, consts, bind)
		}
	}
	return "", false
}

func exprPos(fset *token.FileSet, e ast.Expr) string {
	p := fset.Position(e.Pos())
	return filepath.Base(p.Filename) + ":" + strconv.Itoa(p.Line)
}

// scenariosWithoutArtefact returns "<feature uri>: <scenario>" for every scenario in
// features none of whose steps can attach an artefact according to attaches. An
// undefined step is not this check's concern (TestFeaturesHaveStepDefinitions owns
// that); it simply cannot attach.
func scenariosWithoutArtefact(features []GherkinFeature, attaches map[string]bool) []string {
	var out []string
	for _, f := range features {
		for _, sc := range f.Scenarios {
			ok := false
			for _, st := range sc.Steps {
				if attaches[st.Text] {
					ok = true
					break
				}
			}
			if !ok {
				out = append(out, f.URI+": "+sc.Name)
			}
		}
	}
	return out
}

// TestEveryScenarioCanAttachAnArtefact is the browser-free artefact rule on the unit
// rung: every shipped scenario has at least one step whose definition attaches an
// artefact (shoot/capture/attachText or a direct sr.Embeddings append).
func TestEveryScenarioCanAttachAnArtefact(t *testing.T) {
	idx, err := analyseStepSources(".")
	if err != nil {
		t.Fatalf("analyse step sources: %v", err)
	}
	features, err := ParseFeaturesDir(repoFeaturesDir(t))
	if err != nil {
		t.Fatalf("parse features: %v", err)
	}
	for _, miss := range scenariosWithoutArtefact(features, idx.attaches) {
		t.Errorf("%s — no step in this scenario attaches an artefact; end it with shoot(...) for UI or attachText(...) for CLI evidence (internal/acceptance/README.md)", miss)
	}
}

// TestArtefactAnalysisCoversTheRegistry keeps the static analysis honest: the step
// texts it finds in the source must be exactly the runtime registry's keys, so a new
// registration shape it cannot see fails here instead of silently escaping the rule.
func TestArtefactAnalysisCoversTheRegistry(t *testing.T) {
	idx, err := analyseStepSources(".")
	if err != nil {
		t.Fatalf("analyse step sources: %v", err)
	}
	if len(idx.unresolved) > 0 {
		t.Errorf("step registrations with a non-constant key the analysis cannot read: %v", idx.unresolved)
	}
	var missing, extra []string
	for text := range registry {
		if _, ok := idx.attaches[text]; !ok {
			missing = append(missing, text)
		}
	}
	for text := range idx.attaches {
		if _, ok := registry[text]; !ok {
			extra = append(extra, text)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("registered steps the static analysis did not find (%d): %q", len(missing), missing)
	}
	if len(extra) > 0 {
		t.Errorf("steps the static analysis found that are not registered (%d): %q", len(extra), extra)
	}
}

// TestArtefactCheckFlagsScenarioWithoutAttachingStep is the fixture proof the spec
// asks for: over a tiny step source + feature in testdata, the check flags exactly
// the scenario with no attaching step, and follows attachment through a helper chain,
// a function value passed to a helper, and a closure.
func TestArtefactCheckFlagsScenarioWithoutAttachingStep(t *testing.T) {
	const fx = "testdata/artefactcheck"
	idx, err := analyseStepSources(filepath.Join(fx, "src"))
	if err != nil {
		t.Fatalf("analyse fixture: %v", err)
	}
	wantAttach := map[string]bool{
		"a seeded fixture":                     false,
		"the fixture is asserted":              false,
		"the fixture page is shot":             true, // via a two-deep helper chain
		"the fixture output is attached":       true, // a closure appending directly
		"the fixture output is shot by helper": true, // passed as a value to a helper
	}
	for text, want := range wantAttach {
		got, ok := idx.attaches[text]
		if !ok {
			t.Errorf("fixture step %q not found by the analysis", text)
			continue
		}
		if got != want {
			t.Errorf("fixture step %q attaches = %v, want %v", text, got, want)
		}
	}
	features, err := ParseFeaturesDir(filepath.Join(fx, "features"))
	if err != nil {
		t.Fatalf("parse fixture features: %v", err)
	}
	got := scenariosWithoutArtefact(features, idx.attaches)
	want := []string{fx + "/features/check.feature: A scenario that only asserts"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("flagged scenarios =\n  %q\nwant\n  %q", got, want)
	}
}
