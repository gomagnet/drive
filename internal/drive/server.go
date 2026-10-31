package drive

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"MagnetDrive/internal/middleware"
	"MagnetDrive/web"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gin-gonic/gin"
	"github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
	"go.yaml.in/yaml/v3"
	"golang.org/x/oauth2"
)

//go:embed schema.sql
var schema string

type Config struct {
	TrustedProxies []string `yaml:"trusted_proxies"`
	Listen         string   `yaml:"listen"`
	PublicURL      string   `yaml:"public_url"`
	DataDir        string   `yaml:"data_dir"`
	MaxUpload      int64    `yaml:"max_upload_bytes"`
	Quota          int64    `yaml:"space_quota_bytes"`
	Registration   bool     `yaml:"registration"`
	SFTP           struct {
		Listen  string `yaml:"listen"`
		HostKey string `yaml:"host_key"`
	} `yaml:"sftp"`
	Database struct {
		Host     string
		Port     int
		User     string
		Password string
		Name     string
		TLS      string
	} `yaml:"database"`
	OIDC struct {
		Issuer       string `yaml:"issuer"`
		ClientID     string `yaml:"client_id"`
		ClientSecret string `yaml:"client_secret"`
		Label        string `yaml:"label"`
	} `yaml:"oidc"`
}
type App struct {
	db              *sqlx.DB
	cfg             Config
	oauth           *oauth2.Config
	verifier        *oidc.IDTokenVerifier
	secure          bool
	authSlots       chan struct{}
	uploadSlots     chan struct{}
	loginRate       loginLimiter
	sftpFingerprint string
}
type User struct {
	ID       int64  `db:"id" json:"id"`
	Email    string `db:"email" json:"email"`
	Name     string `db:"name" json:"name"`
	Avatar   string `db:"avatar" json:"avatar"`
	Admin    bool   `db:"is_admin" json:"admin"`
	Theme    string `db:"theme" json:"theme"`
	Language string `db:"language" json:"language"`
}

func Run() error {
	grantEmail := ""
	if len(os.Args) > 1 {
		if len(os.Args) != 4 || os.Args[1] != "admin" || os.Args[2] != "grant" || email(os.Args[3]) == "" {
			return errors.New("usage: MagnetDrive [admin grant EMAIL]")
		}
		grantEmail = email(os.Args[3])
	}
	path := os.Getenv("MAGNETDRIVE_CONFIG")
	if path == "" {
		path = "magnetdrive.yaml"
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("configuration: copy magnetdrive.example.yaml to magnetdrive.yaml and configure MariaDB: %w", err)
	}
	defer f.Close()
	cfg := Config{TrustedProxies: []string{"127.0.0.0/8", "::1/128"}}
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err = dec.Decode(&cfg); err != nil {
		return err
	}
	u, err := url.Parse(cfg.PublicURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.User != nil {
		return errors.New("public_url must be an absolute HTTP(S) origin")
	}
	cfg.PublicURL = strings.TrimRight(cfg.PublicURL, "/")
	if cfg.Listen == "" || cfg.DataDir == "" || cfg.MaxUpload < 1 || cfg.MaxUpload > 1<<40 || cfg.Quota < cfg.MaxUpload || cfg.Database.Host == "" || cfg.Database.Name == "" || cfg.Database.Port < 1 || cfg.Database.Port > 65535 {
		return errors.New("invalid listener, storage limits, or database settings")
	}
	cfg.DataDir, err = filepath.Abs(cfg.DataDir)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(cfg.DataDir, 0700); err != nil {
		return err
	}
	mc := mysql.NewConfig()
	mc.Net = "tcp"
	mc.Addr = net.JoinHostPort(cfg.Database.Host, fmt.Sprint(cfg.Database.Port))
	mc.User = cfg.Database.User
	mc.Passwd = cfg.Database.Password
	mc.DBName = cfg.Database.Name
	mc.ParseTime = true
	mc.Loc = time.UTC
	mc.Collation = "utf8mb4_unicode_ci"
	mc.TLSConfig = cfg.Database.TLS
	mc.Timeout = 10 * time.Second
	mc.ReadTimeout = 30 * time.Second
	mc.WriteTimeout = 30 * time.Second
	db, err := sqlx.Open("mysql", mc.FormatDSN())
	if err != nil {
		return err
	}
	defer db.Close()
	maintenanceStop := make(chan struct{})
	defer close(maintenanceStop)
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-maintenanceStop:
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				for _, table := range []string{"sessions", "login_states", "shares", "invitations"} {
					if _, e := db.ExecContext(ctx, "DELETE FROM "+table+" WHERE expires_at<UTC_TIMESTAMP(6)"); e != nil {
						log.Printf("expiry cleanup %s: %v", table, e)
					}
				}
				cancel()
			}
		}
	}()
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(5 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err = db.PingContext(ctx); err != nil {
		return err
	}
	for _, stmt := range strings.Split(schema, ";") {
		if strings.TrimSpace(stmt) != "" {
			if _, err = db.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("schema: %w", err)
			}
		}
	}
	a := &App{db: db, cfg: cfg, secure: u.Scheme == "https", authSlots: make(chan struct{}, 4), uploadSlots: make(chan struct{}, 4)}
	var nullable string
	if err = db.GetContext(ctx, &nullable, "SELECT IS_NULLABLE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='shares' AND COLUMN_NAME='expires_at'"); err != nil {
		return err
	}
	if nullable == "NO" {
		if _, err = db.ExecContext(ctx, "ALTER TABLE shares MODIFY expires_at DATETIME(6) NULL"); err != nil {
			return err
		}
	}
	if grantEmail != "" {
		res, e := db.ExecContext(ctx, "UPDATE users SET is_admin=TRUE WHERE email=? AND deleted_at IS NULL", grantEmail)
		if e != nil {
			return e
		}
		n, e := res.RowsAffected()
		if e != nil {
			return e
		}
		if n == 0 {
			var id int64
			if e = db.GetContext(ctx, &id, "SELECT id FROM users WHERE email=? AND deleted_at IS NULL", grantEmail); e != nil {
				return errors.New("active account not found")
			}
		}
		log.Printf("Administrator role granted to %s", grantEmail)
		return nil
	}
	if cfg.OIDC.Issuer != "" {
		issuer, parseErr := url.Parse(cfg.OIDC.Issuer)
		if parseErr != nil || issuer.Scheme != "https" || issuer.Host == "" || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" || cfg.OIDC.ClientID == "" {
			return errors.New("OpenID requires an HTTPS issuer URL and a client ID")
		}
		p, e := oidc.NewProvider(ctx, cfg.OIDC.Issuer)
		if e != nil {
			return e
		}
		a.oauth = &oauth2.Config{ClientID: cfg.OIDC.ClientID, ClientSecret: cfg.OIDC.ClientSecret, Endpoint: p.Endpoint(), RedirectURL: cfg.PublicURL + "/auth/oidc/callback", Scopes: []string{oidc.ScopeOpenID, "profile", "email"}}
		a.verifier = p.Verifier(&oidc.Config{ClientID: cfg.OIDC.ClientID})
	}
	stopSFTP, err := a.startSFTP()
	if err != nil {
		return err
	}
	defer stopSFTP()
	gin.SetMode(gin.DebugMode)
	r := gin.Default()
	r.ContextWithFallback = true
	if err = r.SetTrustedProxies(cfg.TrustedProxies); err != nil {
		return fmt.Errorf("trusted_proxies: %w", err)
	}
	r.Use(middleware.ScraperBlocker(), func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("X-Frame-Options", "SAMEORIGIN")
		c.Header("Cache-Control", "no-store")
		c.Header("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' https: data:; media-src 'self'; frame-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'self'")
		if a.secure {
			c.Header("Strict-Transport-Security", "max-age=31536000")
		}
		if c.Request.Method != "GET" && c.Request.Method != "HEAD" {
			if c.GetHeader("Origin") != a.cfg.PublicURL {
				fail(c, 403, "Request origin rejected")
				return
			}
			if c.Request.URL.Path != "/api/upload" {
				c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
			}
		}
		c.Next()
	})
	r.GET("/api/auth", func(c *gin.Context) {
		c.JSON(200, gin.H{"registration": cfg.Registration, "oidc": a.oauth != nil, "oidc_label": cfg.OIDC.Label})
	})
	r.POST("/auth/login", a.login)
	r.POST("/auth/register", a.register)
	r.GET("/invite/:token", a.invitePage)
	r.GET("/api/invitations/:token", a.inviteInfo)
	r.POST("/auth/invitations/:token", a.acceptInvitation)
	r.GET("/auth/oidc", a.oidcStart)
	r.GET("/auth/oidc/callback", a.oidcCallback)
	r.GET("/s/:token", a.publicPage)
	r.GET("/s/:token/content", a.publicContent)
	r.HEAD("/s/:token/content", a.publicContent)
	api := r.Group("/api", a.auth)
	api.GET("/me", func(c *gin.Context) { c.JSON(200, c.MustGet("user")) })
	api.PATCH("/me", a.updateProfile)
	api.POST("/me/password", a.updatePassword)
	admin := api.Group("/admin", a.requireAdmin)
	admin.GET("/users", a.adminUsers)
	admin.POST("/users", a.adminCreateUser)
	admin.PATCH("/users/:id", a.adminUpdateUser)
	admin.DELETE("/users/:id", a.adminDeleteUser)
	admin.PATCH("/spaces/:id", a.adminQuota)
	admin.GET("/invitations", a.adminInvitations)
	admin.POST("/invitations", a.createInvitation)
	admin.DELETE("/invitations/:id", a.revokeInvitation)
	api.POST("/logout", a.logout)
	api.GET("/sftp", a.sftpCredentials)
	api.POST("/sftp/credentials", a.createSFTPCredential)
	api.DELETE("/sftp/credentials/:id", a.deleteSFTPCredential)
	api.GET("/spaces", a.spaces)
	api.POST("/spaces", a.createSpace)
	api.GET("/spaces/:space/members", a.members)
	api.POST("/spaces/:space/members", a.addMember)
	api.DELETE("/spaces/:space/members/:user", a.removeMember)
	api.GET("/entries", a.entries)
	api.POST("/folders", a.folder)
	api.POST("/upload", func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, cfg.MaxUpload+(1<<20))
		a.upload(c)
	})
	api.PATCH("/entries/:id", a.edit)
	api.GET("/entries/:id/path", a.entryPath)
	api.POST("/bulk", a.bulk)
	api.POST("/purge", a.purge)
	api.GET("/entries/:id/content", a.content)
	api.HEAD("/entries/:id/content", a.content)
	api.GET("/entries/:id/versions", a.versions)
	api.POST("/entries/:id/rollback", a.rollback)
	api.GET("/entries/:id/shares", a.shares)
	api.POST("/entries/:id/shares", a.share)
	api.DELETE("/shares/:id", a.revoke)
	assets, _ := fs.Sub(web.Files, ".")
	index, err := web.Files.ReadFile("index.html")
	if err != nil {
		return fmt.Errorf("embedded application page: %w", err)
	}
	// FileFromFS rewrites the request path to /index.html, which Go's
	// FileServer redirects to ./, sending requests for / back to / forever.
	r.GET("/", func(c *gin.Context) { c.Data(http.StatusOK, "text/html; charset=utf-8", index) })
	for _, name := range []string{"i18n.js", "i18n-ru.json", "app.js", "browser.js", "settings.js", "sharing.js", "invitations.js", "scrollbars.js", "scrollbars.css", "style.css", "magnet-icon.png"} {
		r.GET("/"+name, func(c *gin.Context) { c.FileFromFS(strings.TrimPrefix(c.Request.URL.Path, "/"), http.FS(assets)) })
	}
	srv := &http.Server{Addr: cfg.Listen, Handler: r, ReadTimeout: time.Hour, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(quit)
	go func() {
		<-quit
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	log.Printf("MagnetDrive listening on %s", cfg.Listen)
	err = srv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func digest(v string) string                      { s := sha256.Sum256([]byte(v)); return hex.EncodeToString(s[:]) }
func fail(c *gin.Context, status int, msg string) { c.AbortWithStatusJSON(status, gin.H{"error": msg}) }
func problem(c *gin.Context, err error) {
	if err != nil {
		log.Printf("request %s %s: %v", c.Request.Method, c.FullPath(), err)
	}
	fail(c, 500, "Could not complete this operation. Please try again.")
}
func uid(c *gin.Context) int64 { return c.MustGet("user").(User).ID }
func (a *App) auth(c *gin.Context) {
	t, e := c.Cookie("md_session")
	if e != nil {
		fail(c, 401, "Please sign in")
		return
	}
	var u User
	e = a.db.GetContext(c, &u, "SELECT u.id,u.email,u.name,u.avatar,u.is_admin,u.theme,u.language FROM users u JOIN sessions s ON s.user_id=u.id WHERE s.token_hash=? AND s.expires_at>UTC_TIMESTAMP(6) AND u.deleted_at IS NULL", digest(t))
	if e != nil {
		if !errors.Is(e, sql.ErrNoRows) {
			problem(c, e)
		} else {
			fail(c, 401, "Please sign in")
		}
		return
	}
	c.Set("user", u)
	c.Next()
}
func (a *App) cookie(c *gin.Context, name, value string, age int) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(name, value, age, "/", "", a.secure, true)
}
func (a *App) session(c *gin.Context, id int64) error {
	t := randomToken()
	_, err := a.db.ExecContext(c, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES(?,?,?)", digest(t), id, time.Now().UTC().Add(7*24*time.Hour))
	if err == nil {
		a.cookie(c, "md_session", t, 7*86400)
	}
	return err
}
func (a *App) logout(c *gin.Context) {
	t, _ := c.Cookie("md_session")
	if _, e := a.db.ExecContext(c, "DELETE FROM sessions WHERE token_hash=?", digest(t)); e != nil {
		problem(c, e)
		return
	}
	a.cookie(c, "md_session", "", -1)
	c.Status(204)
}
