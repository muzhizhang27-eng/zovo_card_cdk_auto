// Package background tracks work that may outlive an HTTP request.
package background

import (
	"context"
	"sync"
)

var tasks sync.WaitGroup

// Go registers before spawning so shutdown cannot miss queued work.
func Go(fn func()) {
	tasks.Add(1)
	go func() {
		defer tasks.Done()
		fn()
	}()
}

// Wait must be called after HTTP producers have drained and recurring tasks
// have been cancelled. Existing registered tasks may finish child tasks.
func Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() { tasks.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
