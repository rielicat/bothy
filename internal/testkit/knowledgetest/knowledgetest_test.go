package knowledgetest_test

import (
	"testing"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/conformance"
	"github.com/rielicat/bothy/internal/testkit/knowledgetest"
	"github.com/rielicat/bothy/internal/testkit/memstore"
)

func TestConformance(t *testing.T) {
	conformance.Knowledge(t, func(*testing.T) (core.Store, core.KnowledgeRepo) {
		s := memstore.New()
		return s, knowledgetest.New(s)
	})
}
