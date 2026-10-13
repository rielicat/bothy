package agent

import (
	"container/list"
	"context"
	"sync"

	"github.com/bothyhq/bothy/internal/core"
)

// semaphore is a FIFO counting semaphore whose capacity is read on every
// acquire and release, so hot config changes apply to the next waiter.
type semaphore struct {
	limit func() int

	mu      sync.Mutex
	held    int
	waiters list.List // of chan struct{}
}

func newSemaphore(limit func() int) *semaphore { return &semaphore{limit: limit} }

func (s *semaphore) capacity() int { return max(s.limit(), 1) }

// acquire blocks until a slot is free or ctx is done. onWait, when non-nil,
// is called once with the 1-based queue position if the caller has to wait.
func (s *semaphore) acquire(ctx context.Context, onWait func(position int)) error {
	s.mu.Lock()
	if s.waiters.Len() == 0 && s.held < s.capacity() {
		s.held++
		s.mu.Unlock()
		return nil
	}
	ready := make(chan struct{})
	el := s.waiters.PushBack(ready)
	pos := s.waiters.Len()
	s.mu.Unlock()
	if onWait != nil {
		onWait(pos)
	}
	select {
	case <-ready:
		return nil
	case <-ctx.Done():
		s.mu.Lock()
		select {
		case <-ready: // granted while we were giving up
			s.mu.Unlock()
			s.release()
		default:
			s.waiters.Remove(el)
			s.mu.Unlock()
		}
		return context.Cause(ctx)
	}
}

func (s *semaphore) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.held--
	for s.waiters.Len() > 0 && s.held < s.capacity() {
		el := s.waiters.Front()
		s.waiters.Remove(el)
		s.held++
		close(el.Value.(chan struct{}))
	}
}

func (r *Runner) userSem(u core.UserID) *semaphore {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.users[u]
	if !ok {
		s = newSemaphore(func() int { return r.cfg().Agent.MaxRunsPerUser })
		r.users[u] = s
	}
	return s
}
