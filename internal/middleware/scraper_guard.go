package middleware

import (
	"log"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	scraperAttemptWindow = 5 * time.Minute
	scraperBanDuration   = 24 * time.Hour
	scraperAttemptLimit  = 3
	scraperStateLimit    = 20000
)

type scraperState struct {
	windowStart time.Time
	lastSeen    time.Time
	bannedUntil time.Time
	attempts    int
	reason      string
}

type scraperGuard struct {
	mu        sync.Mutex
	state     map[string]scraperState
	lastPrune time.Time
}

var webScraperGuard = &scraperGuard{state: make(map[string]scraperState)}

func (g *scraperGuard) middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := c.ClientIP()
		if ip == "" {
			c.Next()
			return
		}

		now := time.Now()
		g.mu.Lock()
		state := g.state[ip]
		if state.bannedUntil.After(now) {
			g.mu.Unlock()
			c.Header("Cache-Control", "no-store")
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		if !state.bannedUntil.IsZero() {
			delete(g.state, ip)
			state = scraperState{}
		}

		reason := suspiciousScraperPath(c.Request.URL.Path)
		if reason == "" {
			state.lastSeen = now
			if state.attempts > 0 && now.Sub(state.windowStart) > scraperAttemptWindow {
				state = scraperState{lastSeen: now}
			}
			if state.attempts > 0 {
				g.state[ip] = state
			}
			g.pruneLocked(now)
			g.mu.Unlock()
			c.Next()
			return
		}

		if state.attempts == 0 || now.Sub(state.windowStart) > scraperAttemptWindow {
			state = scraperState{windowStart: now}
		}
		state.attempts++
		state.lastSeen = now
		state.reason = reason
		if state.attempts >= scraperAttemptLimit {
			state.bannedUntil = now.Add(scraperBanDuration)
			g.state[ip] = state
			g.pruneLocked(now)
			g.mu.Unlock()
			log.Printf("scraper IP banned ip=%q reason=%q attempts=%d duration=%s", ip, reason, state.attempts, scraperBanDuration)
			c.Header("Cache-Control", "no-store")
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		g.state[ip] = state
		g.pruneLocked(now)
		g.mu.Unlock()
		c.Next()
	}
}

func (g *scraperGuard) pruneLocked(now time.Time) {
	if now.Sub(g.lastPrune) < time.Minute && len(g.state) <= scraperStateLimit {
		return
	}
	g.lastPrune = now
	for ip, state := range g.state {
		if state.bannedUntil.After(now) || (state.attempts > 0 && now.Sub(state.windowStart) <= scraperAttemptWindow) {
			continue
		}
		delete(g.state, ip)
	}
	for len(g.state) > scraperStateLimit {
		var oldestIP string
		var oldest time.Time
		for ip, state := range g.state {
			if oldestIP == "" || state.lastSeen.Before(oldest) {
				oldestIP, oldest = ip, state.lastSeen
			}
		}
		if oldestIP == "" {
			return
		}
		// Keep memory bounded even if a flood creates more active bans than
		// the cache can retain; evict the oldest record as a last resort.
		delete(g.state, oldestIP)
	}
}

func suspiciousScraperPath(requestPath string) string {
	clean := strings.ToLower(path.Clean("/" + requestPath))
	segments := strings.Split(clean, "/")
	for _, segment := range segments {
		switch {
		case segment == ".git":
			return "path:/.git"
		case segment == ".aws":
			return "path:/.aws"
		case segment == ".env" || segment == ".envrc" || strings.HasPrefix(segment, ".env.") || strings.HasPrefix(segment, ".env-"):
			return "path:/.env"
		case segment == "service-account.json" || segment == "serviceaccount.json":
			return "path:/service-account.json"
		case strings.HasPrefix(segment, "aws-exports."):
			return "path:/aws-exports"
		}
	}
	for _, probe := range []string{
		"/rest/api/1.0/application-properties",
		"/ai/credentials",
		"/anthropic/config.json",
		"/claude/config.json",
		"/bedrock/config.json",
		"/azure/secrets.env",
		"/azure/config.json",
		"/aws/credentials",
		"/aws/secrets.env",
		"/aws/secret.env",
		"/aws/secrets.json",
		"/config/azure.php",
		"/actuator/env",
		"/actuator/heapdump",
		"/server-status",
		"/phpinfo.php",
		"/wp-login.php",
	} {
		if clean == probe || strings.HasPrefix(clean, probe+"/") {
			return "path:" + probe
		}
	}
	return ""
}

// ScraperBlocker shares process-local IP scan tracking across HTTP routers.
func ScraperBlocker() gin.HandlerFunc { return webScraperGuard.middleware() }
