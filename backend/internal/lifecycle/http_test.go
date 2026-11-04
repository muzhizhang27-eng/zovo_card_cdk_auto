package lifecycle

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/background"
)

func TestShutdownWaitsForRequestAndItsAsynchronousWork(t *testing.T) {
	started, releaseRequest := make(chan struct{}), make(chan struct{})
	workerStarted, releaseWorker := make(chan struct{}), make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-releaseRequest
		background.Go(func() { close(workerStarted); <-releaseWorker })
		w.WriteHeader(http.StatusOK)
	})}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, server, listener) }()
	responseDone := make(chan struct{})
	go func() {
		defer close(responseDone)
		response, err := http.Get("http://" + listener.Addr().String())
		if err != nil {
			t.Error(err)
			return
		}
		_ = response.Body.Close()
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		t.Fatalf("active request was not drained: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseRequest)
	<-workerStarted
	<-responseDone
	select {
	case err := <-done:
		t.Fatalf("asynchronous request work was not drained: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseWorker)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not finish after all work completed")
	}
}
