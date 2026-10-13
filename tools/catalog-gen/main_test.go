package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestConvertCatwalk(t *testing.T) {
	ps, err := load(context.Background(), "testdata")
	if err != nil {
		t.Fatal(err)
	}
	c, _ := convert(ps, "test", nil)
	if len(c.Providers) != 2 || c.Providers[0].ID != "anthropic" || c.Providers[1].ID != "deepseek" {
		t.Fatalf("providers %+v", c.Providers)
	}
	a := c.Providers[0]
	if a.Kind != "anthropic" || a.BaseURL != "https://api.anthropic.com/v1" {
		t.Fatalf("anthropic %+v", a)
	}
	var sonnet *model
	for i := range a.Models {
		if a.Models[i].ID == "claude-sonnet-4-6" {
			sonnet = &a.Models[i]
		}
	}
	// in_cached is the write price and out_cached the read price.
	if sonnet == nil || !reflect.DeepEqual(*sonnet.Price, price{InPerM: 3_000_000, OutPerM: 15_000_000, CacheReadPerM: 300_000, CacheWritePerM: 3_750_000}) ||
		sonnet.Cache != "explicit" || sonnet.Context != 200000 || !sonnet.Thinking || !sonnet.Vision {
		t.Fatalf("sonnet %+v", sonnet)
	}
	d := c.Providers[1]
	if d.Kind != "openai_compat" || d.BaseURL != "https://api.deepseek.com/v1" {
		t.Fatalf("deepseek %+v", d)
	}
	// DeepSeek lists its cache-hit price under in_cached.
	flash := d.Models[0]
	if flash.ID != "deepseek-v4-flash" || !reflect.DeepEqual(*flash.Price, price{InPerM: 140_000, OutPerM: 280_000, CacheReadPerM: 2_800}) || flash.Cache != "implicit" {
		t.Fatalf("flash %+v %+v", flash, flash.Price)
	}
	for i := 1; i < len(a.Models); i++ {
		if a.Models[i-1].ID >= a.Models[i].ID {
			t.Fatal("models must be sorted for stable diffs")
		}
	}
}

func TestRenderParsesAndIsStable(t *testing.T) {
	ps, err := load(context.Background(), "testdata")
	if err != nil {
		t.Fatal(err)
	}
	c1, _ := convert(ps, "test", nil)
	b1, err := render(c1)
	if err != nil {
		t.Fatal(err)
	}
	c2, _ := convert(ps, "test", nil)
	b2, _ := render(c2)
	if string(b1) != string(b2) {
		t.Fatal("output not deterministic")
	}
	var back catalog
	if err := json.Unmarshal(b1, &back); err != nil || back.Version != 1 || len(back.Providers) != 2 {
		t.Fatalf("%v %+v", err, back)
	}
	if !strings.Contains(string(b1), "\n{\"id\":\"claude-sonnet-4-6\"") {
		t.Fatal("expected one model per line")
	}
}

func TestSkipsUnsupported(t *testing.T) {
	c, _ := convert([]cwProvider{
		{ID: "bedrock", Type: "bedrock", Models: []cwModel{{ID: "x"}}},
		{ID: "envonly", Type: "openai-compat", APIEndpoint: "$SOME_ENDPOINT", Models: []cwModel{{ID: "x"}}},
		{ID: "or", Type: "openrouter", APIEndpoint: "https://openrouter.ai/api/v1/", Models: []cwModel{{ID: "m:free"}, {ID: "unpriced"}, {ID: "m:free"}}},
	}, "t", nil)
	if len(c.Providers) != 1 || c.Providers[0].BaseURL != "https://openrouter.ai/api/v1" {
		t.Fatalf("%+v", c.Providers)
	}
	ms := c.Providers[0].Models
	if len(ms) != 2 || ms[0].ID != "m:free" || ms[0].Price == nil || ms[1].Price != nil {
		t.Fatalf("free models are known $0, unpriced ones unknown: %+v", ms)
	}
}

func TestLoadArrayAndURL(t *testing.T) {
	one, _ := os.ReadFile("testdata/deepseek.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("[" + string(one) + "]"))
	}))
	defer srv.Close()
	ps, err := load(context.Background(), srv.URL)
	if err != nil || len(ps) != 1 || ps[0].ID != "deepseek" {
		t.Fatalf("%v %+v", err, ps)
	}
	if _, err := decode("x", []byte(`{"name":"no id"}`)); err == nil {
		t.Fatal("expected an error for a non-provider object")
	}
}
