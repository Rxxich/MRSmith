//go:build race

package audit

// The race detector slows code down 5-20x, so timing limits do not apply.
const raceEnabled = true
