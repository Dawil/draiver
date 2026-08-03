package web

import (
	"os"
	"strings"
)

// readSpecBody returns the spec markdown with its identity frontmatter stripped.
func readSpecBody(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	s := string(data)
	if strings.HasPrefix(s, "---\n") {
		rest := s[len("---\n"):]
		if i := strings.Index(rest, "\n---\n"); i >= 0 {
			return strings.TrimSpace(rest[i+len("\n---\n"):]), nil
		}
	}
	return strings.TrimSpace(s), nil
}
