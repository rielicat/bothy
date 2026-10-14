package egress

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

const (
	searchTestTimeout = 15 * time.Second
	searchTestQuery   = "bothy web search check"
)

// searchError is a web search failure with its core.FailureCode; Error is
// the message the tool has always shown.
type searchError struct {
	code core.FailureCode
	msg  string
}

func (e *searchError) Error() string                 { return e.msg }
func (e *searchError) FailureCode() core.FailureCode { return e.code }

// transportCode classifies a failure to reach a search service.
func transportCode(err error) core.FailureCode {
	if errors.Is(err, errBlocked) {
		return core.FailPrivateAddress
	}
	if c := core.TransportFailure(err); c != "" {
		return c
	}
	return core.FailUnreachable
}

// TestSearch runs one query against a search provider with these settings
// before they are saved: the SearXNG address and whether it may be private,
// and an API key, or none to use the configured key reference. The error
// carries a core.FailureCode; the result is thrown away.
func (s *Service) TestSearch(ctx context.Context, provider, searxngURL string, allowPrivate bool, key core.Secret) error {
	var t *webSearch
	for _, tool := range s.tools {
		if ws, ok := tool.(*webSearch); ok {
			t = ws
		}
	}
	if t == nil {
		return errNoSearch
	}
	ctx, cancel := context.WithTimeout(ctx, searchTestTimeout)
	defer cancel()
	sc := s.cfg().Search
	var err error
	switch provider {
	case "searxng":
		if searxngURL == "" {
			return fmt.Errorf("%w: the SearXNG address is required", core.ErrInvalid)
		}
		sc.SearxngURL, sc.SearxngAllowPrivate = searxngURL, allowPrivate
		_, err = t.searxng(ctx, sc, searchTestQuery)
	case "brave", "tavily":
		if !key.IsZero() {
			if provider == "brave" {
				_, err = t.braveWith(ctx, key, searchTestQuery, 1)
			} else {
				_, err = t.tavilyWith(ctx, key, searchTestQuery, 1)
			}
		} else if ref := keyRef(sc, provider); ref == "" {
			return &searchError{code: core.FailKeyRejected, msg: provider + ": no API key is saved"}
		} else if provider == "brave" {
			_, err = t.braveSearch(ctx, sc, searchTestQuery, 1)
		} else {
			_, err = t.tavilySearch(ctx, sc, searchTestQuery, 1)
		}
	default:
		return fmt.Errorf("%w: provider must be searxng, brave or tavily", core.ErrInvalid)
	}
	return err
}

func keyRef(sc config.Search, provider string) string {
	if provider == "brave" {
		return sc.BraveAPIKey
	}
	return sc.TavilyAPIKey
}
