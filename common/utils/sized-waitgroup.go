package utils

import (
	"context"
	"math"
	"sync"
)

// SizedWaitGroup has the same role and close to the
// same API as the Golang sync.WaitGroup but adds a limit of
// the amount of goroutines started concurrently.
type SizedWaitGroup struct {
	Size              int
	WaitingEventCount int

	current chan struct{}
	wg      *sync.WaitGroup
}

// New creates a SizedWaitGroup.
// The limit parameter is the maximum amount of
// goroutines which can be started concurrently.
func NewSizedWaitGroup(limit int) *SizedWaitGroup {
	size := math.MaxInt32 // 2^32 - 1
	if limit > 0 {
		size = limit
	}
	return &SizedWaitGroup{
		Size:              size,
		WaitingEventCount: 0,

		current: make(chan struct{}, size),
		wg:      new(sync.WaitGroup),
	}
}

// Add increments the internal WaitGroup counter.
// It can be blocking if the limit of spawned goroutines
// has been reached. It will stop blocking when Done is
// been called.
//
// See sync.WaitGroup documentation for more information.
func (s *SizedWaitGroup) Add(delta ...int) {
	n := 1
	if len(delta) > 0 {
		n = delta[0]
	}

	err := s.AddWithContext(context.Background(), n)
	if err != nil {
		return
	}
	s.WaitingEventCount += n
}

// AddWithContext increments the internal WaitGroup counter.
// It can be blocking if the limit of spawned goroutines
// has been reached. It will stop blocking when Done is
// been called, or when the context is canceled. Returns nil on
// success or an error if the context is canceled before the lock
// is acquired.
//
// See sync.WaitGroup documentation for more information.
func (s *SizedWaitGroup) AddWithContext(ctx context.Context, delta ...int) error {
	n := 1
	if len(delta) > 0 {
		n = delta[0]
	}

	for i := 0; i < n; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case s.current <- struct{}{}:
			break
		}
	}

	s.wg.Add(n)
	s.WaitingEventCount += n
	return nil
}

// Done decrements the SizedWaitGroup counter.
// See sync.WaitGroup documentation for more information.
func (s *SizedWaitGroup) Done() {
	<-s.current
	s.wg.Done()
	s.WaitingEventCount -= 1
}

// Wait blocks until the SizedWaitGroup counter is zero.
// See sync.WaitGroup documentation for more information.
func (s *SizedWaitGroup) Wait() {
	s.wg.Wait()
}
