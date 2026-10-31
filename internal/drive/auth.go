package drive

import (
	"database/sql"
	"errors"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/oauth2"
)

type credentials struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

func email(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	m, e := mail.ParseAddress(v)
	if e != nil || m.Address != v || len(v) > 254 {
		return ""
	}
	return v
}
func (a *App) authSlot(c *gin.Context) bool {
	if !a.loginAllowed(c) {
		return false
	}
	select {
	case a.authSlots <- struct{}{}:
		return true
	default:
		fail(c, 429, "Too many sign-in requests. Try again shortly.")
		return false
	}
}
func (a *App) login(c *gin.Context) {
	if !a.authSlot(c) {
		return
	}
	defer func() { <-a.authSlots }()
	var in credentials
	if c.ShouldBindJSON(&in) != nil || len(in.Password) > 72 {
		fail(c, 400, "Invalid sign-in details")
		return
	}
	var row struct {
		ID   int64
		Hash string `db:"password_hash"`
	}
	err := a.db.GetContext(c, &row, "SELECT id,password_hash FROM users WHERE email=? AND deleted_at IS NULL", email(in.Email))
	hash := row.Hash
	if hash == "" {
		hash = "$2a$12$R9h/cIPz0gi.URNNX3kh2OPST9/PgBkqquzi.Ss7KIUgO2t0jWMUW"
	}
	check := bcrypt.CompareHashAndPassword([]byte(hash), []byte(in.Password))
	if err != nil || row.Hash == "" || check != nil {
		fail(c, 401, "Email or password is incorrect")
		return
	}
	if err = a.session(c, row.ID); err != nil {
		problem(c, err)
		return
	}
	c.Status(204)
}
func (a *App) createUser(c *gin.Context, tx *sqlx.Tx, em, name, avatar, hash string) (int64, error) {
	res, e := tx.ExecContext(c, "INSERT INTO users(email,name,avatar,password_hash) VALUES(?,?,?,?)", em, name, avatar, hash)
	if e != nil {
		return 0, e
	}
	id, e := res.LastInsertId()
	if e != nil {
		return 0, e
	}
	res, e = tx.ExecContext(c, "INSERT INTO spaces(name,owner_id,personal,quota_bytes) VALUES(?,?,TRUE,?)", "My files", id, a.cfg.Quota)
	if e != nil {
		return 0, e
	}
	space, e := res.LastInsertId()
	if e != nil {
		return 0, e
	}
	_, e = tx.ExecContext(c, "INSERT INTO members(space_id,user_id,role) VALUES(?,?,'owner')", space, id)
	return id, e
}
func (a *App) register(c *gin.Context) {
	if !a.cfg.Registration {
		fail(c, 403, "Registration is disabled")
		return
	}
	if !a.authSlot(c) {
		return
	}
	defer func() { <-a.authSlots }()
	var in credentials
	if c.ShouldBindJSON(&in) != nil || email(in.Email) == "" || len(in.Password) < 12 || len(in.Password) > 72 || len(strings.TrimSpace(in.Name)) < 1 || len(in.Name) > 160 {
		fail(c, 400, "Enter a name, valid email, and password of 12–72 bytes")
		return
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(in.Password), 12)
	if e != nil {
		problem(c, e)
		return
	}
	tx, e := a.db.BeginTxx(c, nil)
	if e != nil {
		problem(c, e)
		return
	}
	defer tx.Rollback()
	id, e := a.createUser(c, tx, email(in.Email), strings.TrimSpace(in.Name), "", string(hash))
	if e != nil {
		fail(c, 409, "Account could not be created. Try signing in or use another email.")
		return
	}
	if e = tx.Commit(); e != nil {
		problem(c, e)
		return
	}
	if e = a.session(c, id); e != nil {
		problem(c, e)
		return
	}
	c.Status(201)
}
func (a *App) oidcStart(c *gin.Context) {
	if !a.loginAllowed(c) {
		return
	}
	if a.oauth == nil {
		fail(c, 404, "OpenID is not configured")
		return
	}
	state, nonce, verifier := randomToken(), randomToken(), oauth2.GenerateVerifier()
	_, e := a.db.ExecContext(c, "INSERT INTO login_states(token_hash,nonce,verifier,expires_at) VALUES(?,?,?,?)", digest(state), nonce, verifier, time.Now().UTC().Add(10*time.Minute))
	if e != nil {
		problem(c, e)
		return
	}
	a.cookie(c, "md_oidc", state, 600)
	c.Redirect(302, a.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)))
}
func (a *App) oidcCallback(c *gin.Context) {
	if a.oauth == nil {
		fail(c, 404, "OpenID is not configured")
		return
	}
	cookie, _ := c.Cookie("md_oidc")
	state := c.Query("state")
	a.cookie(c, "md_oidc", "", -1)
	if cookie == "" || cookie != state {
		fail(c, 400, "Sign-in state expired. Start again.")
		return
	}
	tx, e := a.db.BeginTxx(c, nil)
	if e != nil {
		problem(c, e)
		return
	}
	defer tx.Rollback()
	var s struct {
		Nonce    string
		Verifier string
	}
	e = tx.GetContext(c, &s, "SELECT nonce,verifier FROM login_states WHERE token_hash=? AND expires_at>UTC_TIMESTAMP(6) FOR UPDATE", digest(state))
	if e != nil {
		fail(c, 400, "Sign-in expired")
		return
	}
	if _, e = tx.ExecContext(c, "DELETE FROM login_states WHERE token_hash=?", digest(state)); e != nil {
		problem(c, e)
		return
	}
	if e = tx.Commit(); e != nil {
		problem(c, e)
		return
	}
	token, e := a.oauth.Exchange(c, c.Query("code"), oauth2.VerifierOption(s.Verifier))
	if e != nil {
		fail(c, 401, "OpenID exchange failed")
		return
	}
	raw, _ := token.Extra("id_token").(string)
	idToken, e := a.verifier.Verify(c, raw)
	if e != nil || idToken.Nonce != s.Nonce || idToken.Subject == "" {
		fail(c, 401, "OpenID verification failed")
		return
	}
	var claims struct {
		Email    string `json:"email"`
		Verified bool   `json:"email_verified"`
		Name     string `json:"name"`
		Picture  string `json:"picture"`
	}
	if idToken.Claims(&claims) != nil || !claims.Verified || email(claims.Email) == "" {
		fail(c, 403, "OpenID must provide a verified email")
		return
	}
	name := strings.TrimSpace(claims.Name)
	if name == "" {
		name = claims.Email
	}
	if len(name) > 160 {
		name = claims.Email
	}
	picture := ""
	if u, e := url.Parse(claims.Picture); e == nil && u.Scheme == "https" && u.Host != "" && len(claims.Picture) <= 2048 {
		picture = claims.Picture
	}
	tx, e = a.db.BeginTxx(c, nil)
	if e != nil {
		problem(c, e)
		return
	}
	defer tx.Rollback()
	var id int64
	e = tx.GetContext(c, &id, "SELECT user_id FROM identities WHERE issuer=? AND subject=?", a.cfg.OIDC.Issuer, idToken.Subject)
	if errors.Is(e, sql.ErrNoRows) {
		e = tx.GetContext(c, &id, "SELECT id FROM users WHERE email=? FOR UPDATE", email(claims.Email))
		if errors.Is(e, sql.ErrNoRows) {
			id, e = a.createUser(c, tx, email(claims.Email), name, picture, "")
		}
		if e == nil {
			_, e = tx.ExecContext(c, "INSERT INTO identities(issuer,subject,user_id) VALUES(?,?,?)", a.cfg.OIDC.Issuer, idToken.Subject, id)
		}
	}
	if e != nil {
		problem(c, e)
		return
	}
	var activeID int64
	if e = tx.GetContext(c, &activeID, "SELECT id FROM users WHERE id=? AND deleted_at IS NULL FOR UPDATE", id); e != nil {
		fail(c, 401, "Account is no longer available")
		return
	}
	if _, e = tx.ExecContext(c, "UPDATE users SET name=?,avatar=? WHERE id=? AND deleted_at IS NULL", name, picture, id); e != nil {
		problem(c, e)
		return
	}
	if e = tx.Commit(); e != nil {
		problem(c, e)
		return
	}
	if e = a.session(c, id); e != nil {
		problem(c, e)
		return
	}
	c.Redirect(303, "/")
}
