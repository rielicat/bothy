package gemini

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/provider/wire"
)

// Models returns the configured models followed, when discovery is on, by the
// models the API lists for generateContent that are not configured.
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
	req, err := c.o.NewRequest(ctx, http.MethodGet, c.o.URL("/models?pageSize=1000"), nil, setAuth)
	if err != nil {
		return nil, err
	}
	resp, err := c.o.Do(ctx, req, classify)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var list struct {
		Models []struct {
			Name                       string   `json:"name"`
			DisplayName                string   `json:"displayName"`
			InputTokenLimit            int      `json:"inputTokenLimit"`
			OutputTokenLimit           int      `json:"outputTokenLimit"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
			Thinking                   bool     `json:"thinking"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&list); err != nil {
		return nil, c.o.Malformed(err)
	}
	var out []core.ModelInfo
	for _, m := range list.Models {
		if len(m.SupportedGenerationMethods) > 0 && !slices.Contains(m.SupportedGenerationMethods, "generateContent") {
			continue
		}
		out = append(out, core.ModelInfo{
			Ref:     core.ModelRef{Provider: c.o.ID, Model: strings.TrimPrefix(m.Name, "models/")},
			Display: m.DisplayName, Context: m.InputTokenLimit, MaxOutput: m.OutputTokenLimit,
			Tools: true, Vision: true, Thinking: m.Thinking, Cache: core.CacheImplicit, Source: "discovery",
		})
	}
	return out, nil
}
