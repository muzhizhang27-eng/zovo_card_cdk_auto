package handler

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func resetLoginLimits(t *testing.T) {
	t.Helper()
	loginMu.Lock()
	old := loginAttempts
	loginAttempts = make(map[string]*loginAttempt)
	loginLastPrune = time.Time{}
	loginMu.Unlock()
	t.Cleanup(func() { loginMu.Lock(); loginAttempts = old; loginMu.Unlock() })
}

func TestLoginLimitReservesConcurrentAttempts(t *testing.T) {
	resetLoginLimits(t)
	var accepted atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := loginAllowed("192.0.2.1", "admin"); ok {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != loginMaxAttempts {
		t.Fatalf("accepted=%d want=%d", accepted.Load(), loginMaxAttempts)
	}
}

func TestLoginLimitBoundsRotatingUsernamesAndEntryStorage(t *testing.T) {
	resetLoginLimits(t)
	for i := 0; i < loginMaxIPAttempts; i++ {
		if ok, _ := loginAllowed("192.0.2.1", fmt.Sprintf("user-%d", i)); !ok {
			t.Fatalf("rejected attempt %d", i)
		}
	}
	if ok, _ := loginAllowed("192.0.2.1", "another-user"); ok {
		t.Fatal("changing username bypassed the IP limit")
	}
	for i := 0; i < loginMaxEntries; i++ {
		loginAllowed(fmt.Sprintf("ip-%d", i), "username")
	}
	if len(loginAttempts) > loginMaxEntries {
		t.Fatalf("unbounded rate-limit storage: %d", len(loginAttempts))
	}
	for _, entry := range loginAttempts {
		entry.windowStart = time.Now().Add(-loginLockWindow - time.Second)
	}
	if ok, _ := loginAllowed("new-ip", "admin"); !ok {
		t.Fatal("expired entries were not removed")
	}
	if len(loginAttempts) != 2 {
		t.Fatalf("expired entries remain: %d", len(loginAttempts))
	}
}
