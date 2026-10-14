package dto

import "github.com/rielicat/bothy/internal/core"

// ---- knowledge (v0.2 F6) ----

// KnowledgeIndexReq queues the index of a workspace directory
// (POST /api/knowledge/repos; 202 with the core.IndexedRepo).
type KnowledgeIndexReq struct {
	Principal core.PrincipalID `json:"principal_id,omitempty"` // default: the caller's own principal
	Dir       string           `json:"dir"`                    // workspace-relative; "" = the whole workspace
	Repo      string           `json:"repo,omitempty"`         // "owner/name"; default "local/<directory name>"
	SHA       string           `json:"sha,omitempty"`          // optional commit id for citations
}

type KnowledgeSearchResp struct {
	Items []core.KnowledgeHit `json:"items"`
}

// KnowledgeNotesPutReq replaces a repo's notes; If-Match carries the etag
// from GET (`""` while there are no notes yet). Empty content removes them.
type KnowledgeNotesPutReq struct {
	Content string `json:"content"`
}
