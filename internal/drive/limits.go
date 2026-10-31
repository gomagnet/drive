package drive

import (
	"github.com/gin-gonic/gin"
	"sync"
	"time"
)

type rateWindow struct {
	Start time.Time
	Count int
}
type loginLimiter struct {
	mu      sync.Mutex
	windows map[string]rateWindow
}

func (l *loginLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.windows == nil {
		l.windows = make(map[string]rateWindow)
	}
	w := l.windows[key]
	if now.Sub(w.Start) >= time.Minute {
		w = rateWindow{Start: now}
	}
	if w.Count >= 20 {
		return false
	}
	if len(l.windows) >= 4096 {
		for ip, v := range l.windows {
			if now.Sub(v.Start) >= time.Minute {
				delete(l.windows, ip)
			}
		}
		if _, ok := l.windows[key]; !ok && len(l.windows) >= 4096 {
			return false
		}
	}
	w.Count++
	l.windows[key] = w
	return true
}
func (a *App) loginAllowed(c *gin.Context) bool {
	if !a.loginRate.allow(c.ClientIP()) {
		c.Header("Retry-After", "60")
		fail(c, 429, "Too many sign-in attempts. Try again in a minute.")
		return false
	}
	return true
}
