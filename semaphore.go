package main

import (
	"sync"
)

func NewConcurrencyLimiter(n int) *concurrencyLimiter {
	w := &concurrencyLimiter{limit: n}
	w.actors = make(map[string]*actor)
	return w
}

type concurrencyLimiter struct {
	limit  int
	mu     sync.Mutex
	actors map[string]*actor
}

func (s *concurrencyLimiter) TryAcquire(a *actor) bool {
	s.mu.Lock()
	success := len(s.actors) < s.limit
	if success {
		s.actors[a.job.Id.Id] = a
	}
	s.mu.Unlock()
	return success
}

func (s *concurrencyLimiter) SetLimit(l int) {
	s.limit = l
}

func (s *concurrencyLimiter) Limit() int {
	return s.limit
}

func (s *concurrencyLimiter) ReleaseJob(jobID string) {
	s.mu.Lock()
	delete(s.actors, jobID)
	s.mu.Unlock()
}
