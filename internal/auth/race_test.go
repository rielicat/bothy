//go:build race

package auth

// The race detector slows instrumented code unevenly, so wall-clock
// comparisons are meaningless under it.
const raceEnabled = true
