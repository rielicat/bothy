//go:build race

package store

// raceEnabled scales timing budgets: the race detector slows SQLite (pure Go)
// by roughly an order of magnitude.
const raceEnabled = true
