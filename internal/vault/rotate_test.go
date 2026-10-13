package vault

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/rielicat/bothy/internal/core"
)

func TestRotateConcurrentWithUse(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	v := f.open()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for j := range 10 {
				name := fmt.Sprintf("s%d_%d", i, j)
				if _, err := v.Put(ctx, core.GlobalScope, name, []byte("value-"+name), ""); err != nil {
					t.Error(err)
					return
				}
				if s, err := v.Get(ctx, core.GlobalScope, name, "test"); err != nil || s.Reveal() != "value-"+name {
					t.Errorf("Get %s = %q, %v", name, s.Reveal(), err)
					return
				}
			}
		})
	}
	wg.Go(func() {
		for range 3 {
			if err := v.RotateKEK(ctx, ""); err != nil {
				t.Error(err)
			}
		}
	})
	wg.Wait()
	v2 := f.open()
	for i := range 8 {
		for j := range 10 {
			name := fmt.Sprintf("s%d_%d", i, j)
			if got := mustGet(t, v2, core.GlobalScope, name); got != "value-"+name {
				t.Errorf("%s after concurrent rotation = %q", name, got)
			}
		}
	}
}
