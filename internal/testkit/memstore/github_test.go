package memstore

import (
	"testing"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/conformance"
)

func TestGitHubConformance(t *testing.T) {
	conformance.GitHubStore(t, func(*testing.T) core.GitHubStore { return NewGitHub() })
}
