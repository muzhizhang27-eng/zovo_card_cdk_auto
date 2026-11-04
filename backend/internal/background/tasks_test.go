package background

import (
	"context"
	"errors"
	"testing"
)

func TestWaitIncludesRegisteredChildrenAndReportsIncompleteDrain(t *testing.T) {
	childStarted, releaseChild := make(chan struct{}), make(chan struct{})
	Go(func() {
		Go(func() { close(childStarted); <-releaseChild })
	})
	<-childStarted
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("unfinished work reported as drained: %v", err)
	}
	close(releaseChild)
	if err := Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}
