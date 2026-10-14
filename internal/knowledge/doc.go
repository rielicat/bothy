// Package knowledge implements core.Knowledge (v0.2 F6): a read-only index of
// repository checkouts and workspace directories in SQLite FTS5, the
// knowledge_search and notes tools, per-repo project notes kept outside the
// repositories, and the turn-context hook that brings a repo's notes into
// sessions that work on it.
package knowledge
