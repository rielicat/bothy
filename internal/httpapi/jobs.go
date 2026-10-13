package httpapi

import (
	"context"
	"sync"
	"time"
)

// jobs runs background work that outlives its request, at most one per key,
// so repeated clicks cannot pile up model calls or installers.
type jobs struct {
	mu      sync.Mutex
	running map[string]struct{}
	wg      sync.WaitGroup
}

func newJobs() *jobs { return &jobs{running: map[string]struct{}{}} }

// start runs fn in the background unless a job with key is already running;
// it reports whether fn was started. fn gets a context detached from the
// request and bounded by limit.
func (j *jobs) start(parent context.Context, key string, limit time.Duration, fn func(context.Context)) bool {
	j.mu.Lock()
	if _, busy := j.running[key]; busy {
		j.mu.Unlock()
		return false
	}
	j.running[key] = struct{}{}
	j.wg.Add(1)
	j.mu.Unlock()
	go func() {
		defer func() {
			j.mu.Lock()
			delete(j.running, key)
			j.mu.Unlock()
			j.wg.Done()
		}()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), limit)
		defer cancel()
		fn(ctx)
	}()
	return true
}

// wait blocks until every running job has returned (tests).
func (j *jobs) wait() { j.wg.Wait() }
