package dto

import "github.com/rielicat/bothy/internal/core"

// PictureInfo is GET /api/agent/picture/info: whether an admin replaced the
// default picture, and a version that changes with the picture (add it to
// the image address so the browser fetches the new one).
type PictureInfo struct {
	Custom  bool   `json:"custom"`
	Version string `json:"version"`
}

// PictureChange answers PUT and DELETE /api/agent/picture: the picture now
// in use and what happened in each chat app that is turned on.
type PictureChange struct {
	Picture PictureInfo        `json:"picture"`
	Apps    []PictureAppResult `json:"apps"`
}

// PictureAppResult is one chat app's outcome. Detail is one plain sentence
// the UI can show as it is: why it failed and what to do, or where a person
// changes the picture (Slack).
type PictureAppResult struct {
	App    string              `json:"app"`    // telegram | discord | slack
	Result core.PictureOutcome `json:"result"` // updated | failed | manual
	Detail string              `json:"detail"`
}
