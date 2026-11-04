package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/background"
)

const ShutdownTimeout = 300 * time.Second

// Serve drains accepted HTTP requests, then their asynchronous side effects.
// Callers use the same cancelled context for recurring background tasks.
func Serve(ctx context.Context, server *http.Server, listener net.Listener) error {
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	select {
	case err := <-served:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	log.Print("shutdown: draining HTTP requests and background work")
	drain, cancel := context.WithTimeout(context.Background(), ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(drain); err != nil {
		_ = server.Close()
		return fmt.Errorf("HTTP shutdown incomplete; do not treat this as a safe migration drain: %w", err)
	}
	if err := background.Wait(drain); err != nil {
		return fmt.Errorf("background shutdown incomplete; do not treat this as a safe migration drain: %w", err)
	}
	log.Print("shutdown: HTTP requests and background work drained")
	return nil
}
