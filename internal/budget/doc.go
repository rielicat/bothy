// Package budget implements core.Budget: per-run meters (steps, tokens, wall
// time, cost), daily caps with per-user overrides, the 80% warning, the chat
// sender rate limiter and the loop detector. Owner: M3.
package budget
