package ollama

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/wire"
)

// maxShow bounds the /api/show calls of one discovery; a large local library
// should not turn a model list into hundreds of requests. Models past it are
// still listed, without context or capabilities.
const maxShow = 50

// Models returns the configured models followed, when discovery is on, by the
// local models that are not configured.
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

func (c *Client) getJSON(ctx context.Context, method, path string, body, out any) error {
	req, err := c.o.NewRequest(ctx, method, c.o.URL(path), body, setAuth)
	if err != nil {
		return err
	}
	resp, err := c.o.Do(ctx, req, classify)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out); err != nil {
		return c.o.Malformed(err)
	}
	return nil
}

// Tags lists GET /api/tags.
func (c *Client) Tags(ctx context.Context) ([]string, error) {
	var list struct {
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := c.getJSON(ctx, http.MethodGet, "/api/tags", nil, &list); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(list.Models))
	for _, m := range list.Models {
		name := m.Model
		if name == "" {
			name = m.Name
		}
		out = append(out, name)
	}
	return out, nil
}

// ShowInfo is what /api/show tells about a model.
type ShowInfo struct {
	Context      int // "<arch>.context_length": the trained maximum
	Capabilities []string
}

// Show calls POST /api/show.
func (c *Client) Show(ctx context.Context, model string) (ShowInfo, error) {
	var resp struct {
		ModelInfo    map[string]json.RawMessage `json:"model_info"`
		Capabilities []string                   `json:"capabilities"`
	}
	if err := c.getJSON(ctx, http.MethodPost, "/api/show", map[string]string{"model": model}, &resp); err != nil {
		return ShowInfo{}, err
	}
	info := ShowInfo{Capabilities: resp.Capabilities}
	var arch string
	_ = json.Unmarshal(resp.ModelInfo["general.architecture"], &arch)
	if arch != "" {
		_ = json.Unmarshal(resp.ModelInfo[arch+".context_length"], &info.Context)
	}
	if info.Context == 0 {
		for k, v := range resp.ModelInfo {
			if strings.HasSuffix(k, ".context_length") && json.Unmarshal(v, &info.Context) == nil && info.Context > 0 {
				break
			}
		}
	}
	return info, nil
}

// Running calls GET /api/ps and returns the context each loaded model was
// allocated (0 when the server does not report it).
func (c *Client) Running(ctx context.Context) (map[string]int, error) {
	var resp struct {
		Models []struct {
			Name          string `json:"name"`
			Model         string `json:"model"`
			ContextLength int    `json:"context_length"`
		} `json:"models"`
	}
	if err := c.getJSON(ctx, http.MethodGet, "/api/ps", nil, &resp); err != nil {
		return nil, err
	}
	out := make(map[string]int, len(resp.Models))
	for _, m := range resp.Models {
		out[m.Name] = m.ContextLength
		if m.Model != "" {
			out[m.Model] = m.ContextLength
		}
	}
	return out, nil
}

// Info builds a ModelInfo from /api/show (vision, tools and thinking come
// from the advertised capabilities).
func (c *Client) Info(model string, s ShowInfo) core.ModelInfo {
	return core.ModelInfo{
		Ref:      core.ModelRef{Provider: c.o.ID, Model: model},
		Context:  s.Context,
		Tools:    len(s.Capabilities) == 0 || slices.Contains(s.Capabilities, "tools"),
		Vision:   slices.Contains(s.Capabilities, "vision"),
		Thinking: slices.Contains(s.Capabilities, "thinking"),
		Local:    true,
		Source:   "discovery",
	}
}

// Discover lists the local models with their context and capabilities.
func (c *Client) Discover(ctx context.Context) ([]core.ModelInfo, error) {
	names, err := c.Tags(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]core.ModelInfo, len(names))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, name := range names {
		if i >= maxShow {
			out[i] = basicInfo(c.o.ID, name)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			s, err := c.Show(ctx, name)
			if err != nil {
				out[i] = basicInfo(c.o.ID, name)
				return
			}
			out[i] = c.Info(name, s)
		}()
	}
	wg.Wait()
	return out, nil
}

func basicInfo(id, model string) core.ModelInfo {
	return core.ModelInfo{Ref: core.ModelRef{Provider: id, Model: model}, Tools: true, Local: true, Source: "discovery"}
}
