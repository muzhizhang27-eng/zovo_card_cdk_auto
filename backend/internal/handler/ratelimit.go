package handler

import (
	"sync"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/auth"
)

// 登录限流同时限制单 IP 和 IP+用户名，尝试在执行密码校验前计入窗口。
const (
	loginMaxAttempts   = 5
	loginMaxIPAttempts = 20
	loginMaxEntries    = 4096
	loginLockWindow    = 15 * time.Minute
)

type loginAttempt struct {
	failures    int
	lockedUntil time.Time
	windowStart time.Time
}

var (
	loginMu        sync.Mutex
	loginAttempts  = make(map[string]*loginAttempt)
	loginLastPrune time.Time
)

// loginAllowed 返回是否允许尝试登录，以及若被锁定还需等待多久。
func loginAllowed(ip, username string) (bool, time.Duration) {
	loginMu.Lock()
	defer loginMu.Unlock()
	now := time.Now()
	if now.Sub(loginLastPrune) >= time.Minute || len(loginAttempts) >= loginMaxEntries-2 {
		for key, attempt := range loginAttempts {
			if now.Sub(attempt.windowStart) >= loginLockWindow {
				delete(loginAttempts, key)
			}
		}
		loginLastPrune = now
	}
	keys := []string{"ip:" + auth.SecretDigest(ip), "pair:" + auth.SecretDigest(ip+"|"+username)}
	limits := []int{loginMaxIPAttempts, loginMaxAttempts}
	missing := 0
	for i, key := range keys {
		a := loginAttempts[key]
		if a != nil && now.Sub(a.windowStart) >= loginLockWindow {
			delete(loginAttempts, key)
			a = nil
		}
		if a == nil {
			missing++
		} else if a.failures >= limits[i] {
			return false, a.windowStart.Add(loginLockWindow).Sub(now)
		}
	}
	if len(loginAttempts)+missing > loginMaxEntries {
		return false, time.Minute
	}
	// Reserve attempts before password work, so simultaneous requests cannot
	// all pass the limit while earlier bcrypt checks are still running.
	for _, key := range keys {
		if loginAttempts[key] == nil {
			loginAttempts[key] = &loginAttempt{windowStart: now}
		}
		loginAttempts[key].failures++
	}
	return true, 0
}

// loginSucceeded 清除该账号的尝试计数，保留 IP 总预算以限制轮换用户名。
func loginSucceeded(ip, username string) {
	loginMu.Lock()
	defer loginMu.Unlock()
	delete(loginAttempts, "pair:"+auth.SecretDigest(ip+"|"+username))
}
