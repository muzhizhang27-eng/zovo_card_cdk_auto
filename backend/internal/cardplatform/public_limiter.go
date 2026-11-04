package cardplatform

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Browser sessions on this server share the same upstream IP quota.
var sharedPublicLimiter = &publicLimiter{buckets: make(map[string]*publicBucket)}

type publicLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*publicBucket
	interval time.Duration // nonzero for a custom limiter, e.g. in tests
}

type publicBucket struct {
	gate chan struct{}
	mu   sync.Mutex
	next time.Time
}

func publicInterval(path string) time.Duration {
	switch strings.SplitN(path, "?", 2)[0] {
	case "/preview", "/preflight":
		return 2100 * time.Millisecond
	case "/redeem":
		return 3100 * time.Millisecond
	case "/result":
		return 1100 * time.Millisecond
	default:
		return 0
	}
}

func (l *publicLimiter) bucket(base, path string) *publicBucket {
	key := base + "|" + strings.SplitN(path, "?", 2)[0]
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buckets == nil {
		l.buckets = make(map[string]*publicBucket)
	}
	if l.buckets[key] == nil {
		l.buckets[key] = &publicBucket{gate: make(chan struct{}, 1)}
	}
	return l.buckets[key]
}

func (l *publicLimiter) wait(ctx context.Context, base, path string) error {
	interval := publicInterval(path)
	if interval == 0 {
		return ctx.Err()
	}
	if l.interval > 0 {
		interval = l.interval
	}
	b := l.bucket(base, path)
	select {
	case b.gate <- struct{}{}:
		defer func() { <-b.gate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	for {
		b.mu.Lock()
		delay := time.Until(b.next)
		if delay <= 0 {
			if err := ctx.Err(); err != nil {
				b.mu.Unlock()
				return err
			}
			b.next = time.Now().Add(interval)
			b.mu.Unlock()
			return nil
		}
		b.mu.Unlock()
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
			// A concurrent 429 response may have extended the cooldown.
		}
	}
}

func (l *publicLimiter) cooldown(base, path string, delay time.Duration) {
	b := l.bucket(base, path)
	b.mu.Lock()
	defer b.mu.Unlock()
	if until := time.Now().Add(delay); until.After(b.next) {
		b.next = until
	}
}

func retryAfter(header string, attempt int) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(header); err == nil {
		if delay := time.Until(at); delay > 0 {
			return delay
		}
		return 0
	}
	return time.Second * time.Duration(2<<attempt)
}
