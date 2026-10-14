package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/wire"
)

// Models returns the configured models followed, when discovery is on, by the
// models GET /models lists that are not configured.
func (c *Client) Models(ctx context.Context) ([]core.ModelInfo, error) { return models(ctx, &c.o) }

// Discover lists GET /models.
func (c *Client) Discover(ctx context.Context) ([]core.ModelInfo, error) { return discover(ctx, &c.o) }

func models(ctx context.Context, o *wire.Options) ([]core.ModelInfo, error) {
	out := append([]core.ModelInfo(nil), o.Models...)
	if !o.Discover {
		return out, nil
	}
	found, err := discover(ctx, o)
	if err != nil {
		return out, err
	}
	return wire.MergeModels(out, found), nil
}

// discover lists GET /models. OpenRouter adds context_length and vLLM
// max_model_len; plain OpenAI and LM Studio report no window.
func discover(ctx context.Context, o *wire.Options) ([]core.ModelInfo, error) {
	req, err := o.NewRequest(ctx, http.MethodGet, o.URL("/models"), nil, setAuth)
	if err != nil {
		return nil, err
	}
	resp, err := o.Do(ctx, req, classify)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var list struct {
		Data []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			ContextLength int    `json:"context_length"`
			MaxModelLen   int    `json:"max_model_len"`
			// OpenRouter only: what the model returns and which request
			// parameters it takes ("tools" among them when it calls tools).
			Architecture *struct {
				OutputModalities []string `json:"output_modalities"`
			} `json:"architecture"`
			SupportedParameters []string `json:"supported_parameters"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&list); err != nil {
		return nil, o.Malformed(err)
	}
	out := make([]core.ModelInfo, 0, len(list.Data))
	for _, m := range list.Data {
		// A router's batch variants answer hours later, and image or audio
		// models and models that cannot call tools cannot run the agent.
		if strings.HasSuffix(m.ID, ":batch") ||
			m.Architecture != nil && len(m.Architecture.OutputModalities) > 0 && !slices.Contains(m.Architecture.OutputModalities, "text") ||
			m.SupportedParameters != nil && !slices.Contains(m.SupportedParameters, "tools") {
			continue
		}
		ctxLen := m.ContextLength
		if ctxLen == 0 {
			ctxLen = m.MaxModelLen
		}
		out = append(out, core.ModelInfo{
			Ref:     core.ModelRef{Provider: o.ID, Model: m.ID},
			Display: m.Name, Context: ctxLen, Tools: true, Source: "discovery",
		})
	}
	return out, nil
}
