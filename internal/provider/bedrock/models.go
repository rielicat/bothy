package bedrock

import (
	"cmp"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/wire"
)

// maxProfilePages bounds ListInferenceProfiles pagination.
const maxProfilePages = 20

// listBodyLimit bounds one listing response.
const listBodyLimit = 8 << 20

type foundationModel struct {
	ModelID                    string   `json:"modelId"`
	ModelName                  string   `json:"modelName"`
	ProviderName               string   `json:"providerName"`
	InputModalities            []string `json:"inputModalities"`
	OutputModalities           []string `json:"outputModalities"`
	ResponseStreamingSupported bool     `json:"responseStreamingSupported"`
	InferenceTypesSupported    []string `json:"inferenceTypesSupported"`
	ModelLifecycle             struct {
		Status string `json:"status"`
	} `json:"modelLifecycle"`
}

type inferenceProfile struct {
	InferenceProfileID   string `json:"inferenceProfileId"`
	InferenceProfileName string `json:"inferenceProfileName"`
	Status               string `json:"status"`
	Type                 string `json:"type"`
}

// Models returns the configured models followed, when discovery is on, by
// the usable models the account lists.
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

// Discover lists the account's usable Claude and Nova text models in the
// region: the active system-defined inference profiles (us., eu., global.…)
// and the foundation models invocable on demand by their bare id.
// Speech-only models (Sonic) and models without streamed text output are
// left out; models the listing marks LEGACY (still served, retiring) come
// last with "legacy" in their name.
func (c *Client) Discover(ctx context.Context) ([]core.ModelInfo, error) {
	fms, ferr := c.foundationModels(ctx)
	profiles, perr := c.inferenceProfiles(ctx)
	if ferr != nil && perr != nil {
		return nil, ferr
	}
	byID := map[string]foundationModel{}
	for _, m := range fms {
		byID[m.ModelID] = m
	}
	var out []core.ModelInfo
	legacy := map[string]bool{}
	add := func(id, display string, fm foundationModel, known bool) {
		_, base := SplitModel(id)
		if !isClaude(base) && !isNova(base) || strings.Contains(base, "sonic") {
			return
		}
		if known && (!slices.Contains(fm.InputModalities, "TEXT") || !slices.Contains(fm.OutputModalities, "TEXT") ||
			!fm.ResponseStreamingSupported) {
			return
		}
		legacy[id] = known && isLegacy(fm)
		vision := SupportsVision(id)
		if known {
			vision = slices.Contains(fm.InputModalities, "IMAGE")
		}
		info := core.ModelInfo{Ref: core.ModelRef{Provider: c.o.ID, Model: id}, Display: display,
			Tools: true, Vision: vision, Source: "discovery"}
		if SupportsCache(id) {
			info.Cache = core.CacheExplicit
		}
		out = append(out, info)
	}
	for _, p := range profiles {
		if p.Status != "ACTIVE" || p.InferenceProfileID == "" {
			continue
		}
		geo, base := SplitModel(p.InferenceProfileID)
		fm, known := byID[base]
		display := p.InferenceProfileName
		if known && fm.ModelName != "" {
			label := geoLabel(geo)
			if isLegacy(fm) {
				label += ", legacy"
			}
			display = fm.ModelName + " (" + label + ")"
		}
		add(p.InferenceProfileID, display, fm, known)
	}
	for _, m := range fms {
		if slices.Contains(m.InferenceTypesSupported, "ON_DEMAND") {
			display := m.ModelName
			if isLegacy(m) {
				display += " (legacy)"
			}
			add(m.ModelID, display, m, true)
		}
	}
	rank := func(m core.ModelInfo) int {
		r := familyRank(m.Ref.Model) * 2
		if legacy[m.Ref.Model] {
			r++
		}
		return r
	}
	slices.SortFunc(out, func(a, b core.ModelInfo) int {
		return cmp.Or(cmp.Compare(rank(a), rank(b)), cmp.Compare(a.Display, b.Display), cmp.Compare(a.Ref.Model, b.Ref.Model))
	})
	return out, nil
}

func isLegacy(fm foundationModel) bool { return fm.ModelLifecycle.Status == "LEGACY" }

func familyRank(id string) int {
	if _, base := SplitModel(id); isClaude(base) {
		return 0
	}
	return 1
}

func geoLabel(geo string) string {
	switch geo {
	case "global":
		return "Global"
	case "":
		return "this region"
	case "apac":
		return "Asia Pacific"
	case "us-gov":
		return "US GovCloud"
	default:
		return strings.ToUpper(geo)
	}
}

func (c *Client) getJSON(ctx context.Context, path string, q url.Values, v any) error {
	resp, err := c.send(ctx, http.MethodGet, c.o.ControlURL, path, q, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, listBodyLimit)).Decode(v); err != nil {
		return c.o.Malformed(err)
	}
	return nil
}

// foundationModels calls ListFoundationModels for streamed text output.
func (c *Client) foundationModels(ctx context.Context) ([]foundationModel, error) {
	var list struct {
		ModelSummaries []foundationModel `json:"modelSummaries"`
	}
	if err := c.getJSON(ctx, "/foundation-models", url.Values{"byOutputModality": {"TEXT"}}, &list); err != nil {
		return nil, err
	}
	return list.ModelSummaries, nil
}

// inferenceProfiles calls ListInferenceProfiles for the system-defined
// (cross-region) profiles, following nextToken.
func (c *Client) inferenceProfiles(ctx context.Context) ([]inferenceProfile, error) {
	var out []inferenceProfile
	token := ""
	for page := 0; page < maxProfilePages; page++ {
		q := url.Values{"maxResults": {"1000"}, "type": {"SYSTEM_DEFINED"}}
		if token != "" {
			q.Set("nextToken", token)
		}
		var list struct {
			Summaries []inferenceProfile `json:"inferenceProfileSummaries"`
			NextToken string             `json:"nextToken"`
		}
		if err := c.getJSON(ctx, "/inference-profiles", q, &list); err != nil {
			return out, err
		}
		out = append(out, list.Summaries...)
		if list.NextToken == "" {
			break
		}
		token = list.NextToken
	}
	return out, nil
}
