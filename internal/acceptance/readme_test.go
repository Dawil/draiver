package acceptance

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// helperRow matches a README helper-table row: | `name` | `file.go:line` | …
var helperRow = regexp.MustCompile("^\\|\\s*`(\\w+)`\\s*\\|\\s*`([\\w./]+\\.go):(\\d+)`\\s*\\|")

// TestReadmeHelperTableIsCurrent keeps README.md's helper table from rotting (drv-060):
// every `file:line` must point at the declaration of the named helper — a func, a
// method-less `var`, or a `type` — so an agent that jumps to the line lands on it.
func TestReadmeHelperTableIsCurrent(t *testing.T) {
	raw, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	rows := 0
	for _, line := range strings.Split(string(raw), "\n") {
		m := helperRow.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		rows++
		name, file := m[1], m[2]
		n, _ := strconv.Atoi(m[3])
		src, err := os.ReadFile(file)
		if err != nil {
			t.Errorf("README row %q: %v", name, err)
			continue
		}
		lines := strings.Split(string(src), "\n")
		if n < 1 || n > len(lines) {
			t.Errorf("README row %q: %s:%d is past the end of the file (%d lines)", name, file, n, len(lines))
			continue
		}
		decl := regexp.MustCompile(`^(func|var|type) ` + regexp.QuoteMeta(name) + `\b`)
		if got := lines[n-1]; !decl.MatchString(got) {
			t.Errorf("README row %q: %s:%d is %q, want `func|var|type %s` — update the line number", name, file, n, strings.TrimSpace(got), name)
		}
	}
	if rows < 10 {
		t.Fatalf("found only %d helper-table rows in README.md; did the table format change?", rows)
	}
}
