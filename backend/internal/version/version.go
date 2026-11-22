// Package version holds the release number. Bump VERSION with every change
// (1.0.0 → 1.0.1 → …) and describe it in CHANGELOG.md.
package version

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var raw string

// Version is the current release, e.g. "1.0.3".
var Version = strings.TrimSpace(raw)
