package main

import (
	_ "embed"

	"github.com/Dawil/draiver/cmd"
	"github.com/Dawil/draiver/internal/version"
)

// changelogMD is the project changelog, embedded so the binary's version *is*
// the changelog's — drift is impossible. The embed must live in this repo-root
// package: go:embed cannot reference a parent directory, so cmd/ and internal/
// cannot reach CHANGELOG.md themselves.
//
//go:embed CHANGELOG.md
var changelogMD string

func init() {
	// The embedded changelog is checked in beside this file, so a parse failure is
	// a build-time regression, not a runtime input; leave the "dev" fallback in
	// place rather than crash a working binary over it.
	if v, err := version.FromChangelog(changelogMD); err == nil {
		cmd.SetVersion(v)
	}
}
