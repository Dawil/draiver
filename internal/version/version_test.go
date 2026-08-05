package version

import "testing"

func TestFromChangelog(t *testing.T) {
	tests := []struct {
		name      string
		changelog string
		want      string
		wantErr   bool
	}{
		{
			name: "skips Unreleased, returns topmost released",
			changelog: `# Changelog

## [Unreleased]

### Added
- something new

## [0.2.1] - 2026-08-05

### Added
- a thing

## [0.2.0] - 2026-08-05
`,
			want: "0.2.1",
		},
		{
			name: "no Unreleased section",
			changelog: `# Changelog

## [0.1.0] - 2026-08-03
`,
			want: "0.1.0",
		},
		{
			name: "released header without a date still parses",
			changelog: `## [Unreleased]
## [1.4.2]
`,
			want: "1.4.2",
		},
		{
			name: "a bracketed link earlier in the line is not a header",
			changelog: `See [the format] docs.

## [0.9.0] - 2026-01-01
`,
			want: "0.9.0",
		},
		{
			name: "only Unreleased is an error",
			changelog: `## [Unreleased]

### Added
- pending work
`,
			wantErr: true,
		},
		{
			name:      "empty document is an error",
			changelog: "",
			wantErr:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FromChangelog(tt.changelog)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("FromChangelog() = %q, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("FromChangelog() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("FromChangelog() = %q, want %q", got, tt.want)
			}
		})
	}
}
