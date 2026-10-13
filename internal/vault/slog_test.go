package vault

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/core"
)

type wrapper struct{ Token string }

type stringer struct{ v string }

func (s stringer) String() string { return "stringer(" + s.v + ")" }

type hiddenField struct {
	Name   string
	Secret string `json:"-"`
}

func TestSecretsNeverReachSlog(t *testing.T) {
	f := newFixture(t)
	value := "VAULT-CANARY-9f8e7d6c5b4a"
	v := f.open()
	mustPut(t, v, core.GlobalScope, "canary", value)
	got, err := v.Get(context.Background(), core.GlobalScope, "canary", "test")
	if err != nil {
		t.Fatal(err)
	}
	forms := []string{value, base64.StdEncoding.EncodeToString([]byte(value)), hex.EncodeToString([]byte(value))}

	for _, name := range []string{"text", "json"} {
		var buf bytes.Buffer
		var base slog.Handler = slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
		if name == "json" {
			base = slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
		}
		log := slog.New(NewSlogHandler(base, v.red))
		log.Info("calling with "+got.Reveal(), "key", got.Reveal(), "b64", forms[1])
		log.With("ctx", got.Reveal()).WithGroup("g").Warn("grouped", slog.Group("inner", "hex", forms[2], "n", 3))
		log.Error("failed", "err", fmt.Errorf("upstream rejected %s", got.Reveal()), "wrapped", errors.New(value))
		log.Debug("values", "struct", wrapper{Token: value}, "stringer", stringer{value}, "bytes", []byte(value),
			"hidden", hiddenField{Name: "n", Secret: value}, "secret", got, "map", map[string]string{"k": value})
		log.Info("plain", "count", 7, "ok", true, "nil", nil)
		out := buf.String()
		for _, form := range forms {
			if strings.Contains(out, form) {
				t.Errorf("%s handler leaked %q:\n%s", name, form, out)
			}
		}
		if !strings.Contains(out, Mask) || !strings.Contains(out, "count") {
			t.Errorf("%s handler output looks wrong:\n%s", name, out)
		}
	}
	if NewSlogHandler(slog.DiscardHandler, nil) != slog.DiscardHandler {
		t.Error("a nil redactor should leave the handler unwrapped")
	}
}
