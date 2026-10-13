package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/wire"
)

// Models returns the configured models followed, when discovery is on, by the
// models the API lists that are not configured.
func (c *Client) Models(ctx context.Context) ([]core.ModelInfo, error) {
	out := append([]core.ModelInfo(nil), c.o.Models...)
	if !c.o.Discover {
		return out, nil
	}
	found, err := c.Discover(ctx)
	if err != nil {
		return out, err
	}
	return wire.MergeModels(out, found), nil
}

// Discover lists GET /models.
func (c *Client) Discover(ctx context.Context) ([]core.ModelInfo, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/models?limit=1000", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.o.Do(ctx, req, classify)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var list struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&list); err != nil {
		return nil, c.o.Malformed(err)
	}
	out := make([]core.ModelInfo, 0, len(list.Data))
	for _, m := range list.Data {
		out = append(out, core.ModelInfo{
			Ref:     core.ModelRef{Provider: c.o.ID, Model: m.ID},
			Display: m.DisplayName, Tools: true, Cache: core.CacheExplicit, Source: "discovery",
		})
	}
	return out, nil
}
