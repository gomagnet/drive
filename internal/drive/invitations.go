package drive

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"MagnetDrive/web"
	"github.com/gin-gonic/gin"
	"github.com/go-sql-driver/mysql"
	"golang.org/x/crypto/bcrypt"
)

type Invitation struct {
	ID        int64     `db:"id" json:"id"`
	Email     string    `db:"email" json:"email"`
	SpaceID   *int64    `db:"space_id" json:"space_id"`
	SpaceName string    `db:"space_name" json:"space_name"`
	Role      string    `db:"role" json:"role"`
	Expires   time.Time `db:"expires_at" json:"expires_at"`
	Created   time.Time `db:"created_at" json:"created_at"`
}

const invitationSelect = `SELECT i.id,i.email,i.space_id,COALESCE(s.name,'') space_name,i.role,i.expires_at,i.created_at FROM invitations i JOIN users creator ON creator.id=i.created_by LEFT JOIN spaces s ON s.id=i.space_id WHERE i.used_at IS NULL AND i.expires_at>UTC_TIMESTAMP(6) AND creator.is_admin=TRUE AND creator.deleted_at IS NULL`

func (a *App) adminInvitations(c *gin.Context) {
	list := []Invitation{}
	if e := a.db.SelectContext(c, &list, invitationSelect+" ORDER BY i.created_at DESC,i.id DESC LIMIT 1000"); e != nil {
		problem(c, e)
		return
	}
	c.JSON(200, list)
}
func (a *App) createInvitation(c *gin.Context) {
	var in struct {
		Email string `json:"email"`
		Space *int64 `json:"space_id"`
		Role  string `json:"role"`
		Hours int    `json:"hours"`
	}
	if c.ShouldBindJSON(&in) != nil || (in.Email != "" && email(in.Email) == "") || in.Hours < 1 || in.Hours > 168 || (in.Role != "viewer" && in.Role != "editor") {
		fail(c, 400, "Enter an optional valid email, viewer/editor access, and an expiry of 1–168 hours")
		return
	}
	tx, e := a.adminTx(c)
	if e != nil {
		problem(c, e)
		return
	}
	defer tx.Rollback()
	if in.Email != "" {
		var count int
		if e = tx.GetContext(c, &count, "SELECT COUNT(*) FROM users WHERE email=? AND deleted_at IS NULL", email(in.Email)); e != nil {
			problem(c, e)
			return
		}
		if count > 0 {
			fail(c, 409, "This email already has an account. Add the existing user through space members.")
			return
		}
	}
	if in.Space != nil {
		var personal bool
		if e = tx.GetContext(c, &personal, "SELECT personal FROM spaces WHERE id=? FOR UPDATE", *in.Space); e != nil || personal {
			fail(c, 400, "Choose an available shared space")
			return
		}
	}
	var pending int
	if e = tx.GetContext(c, &pending, "SELECT COUNT(*) FROM invitations i JOIN users creator ON creator.id=i.created_by WHERE i.used_at IS NULL AND i.expires_at>UTC_TIMESTAMP(6) AND creator.is_admin=TRUE AND creator.deleted_at IS NULL"); e != nil {
		problem(c, e)
		return
	}
	if pending >= 1000 {
		fail(c, 409, "Revoke pending invitations before creating more (limit: 1000)")
		return
	}
	token := randomToken()
	expires := time.Now().UTC().Add(time.Duration(in.Hours) * time.Hour)
	bound := ""
	if in.Email != "" {
		bound = email(in.Email)
	}
	res, e := tx.ExecContext(c, "INSERT INTO invitations(token_hash,email,space_id,role,created_by,expires_at) VALUES(?,?,?,?,?,?)", digest(token), bound, in.Space, in.Role, uid(c), expires)
	if e != nil {
		problem(c, e)
		return
	}
	id, e := res.LastInsertId()
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		problem(c, e)
		return
	}
	c.JSON(201, gin.H{"id": id, "url": a.cfg.PublicURL + "/invite/" + token, "expires_at": expires})
}
func (a *App) revokeInvitation(c *gin.Context) {
	tx, e := a.adminTx(c)
	if e != nil {
		problem(c, e)
		return
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(c, "DELETE FROM invitations WHERE id=?", number(c.Param("id"))); e == nil {
		e = tx.Commit()
	}
	if e != nil {
		problem(c, e)
		return
	}
	c.Status(204)
}
func (a *App) invitePage(c *gin.Context) {
	if len(c.Param("token")) != 64 {
		fail(c, 404, "Invitation is unavailable or expired")
		return
	}
	page, e := web.Files.ReadFile("index.html")
	if e != nil {
		problem(c, e)
		return
	}
	c.Data(200, "text/html; charset=utf-8", page)
}
func (a *App) inviteInfo(c *gin.Context) {
	if !a.loginAllowed(c) {
		return
	}
	if len(c.Param("token")) != 64 {
		fail(c, 404, "Invitation is unavailable or expired")
		return
	}
	var invite Invitation
	e := a.db.GetContext(c, &invite, invitationSelect+" AND i.token_hash=?", digest(c.Param("token")))
	if e != nil {
		if errors.Is(e, sql.ErrNoRows) {
			fail(c, 404, "Invitation is unavailable or expired")
		} else {
			problem(c, e)
		}
		return
	}
	c.JSON(200, invite)
}
func (a *App) acceptInvitation(c *gin.Context) {
	if !a.authSlot(c) {
		return
	}
	defer func() { <-a.authSlots }()
	if len(c.Param("token")) != 64 {
		fail(c, 404, "Invitation is unavailable or expired")
		return
	}
	var in credentials
	if c.ShouldBindJSON(&in) != nil || email(in.Email) == "" || strings.TrimSpace(in.Name) == "" || len(in.Name) > 160 || len(in.Password) < 12 || len(in.Password) > 72 {
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
	var serial int
	if e = tx.GetContext(c, &serial, "SELECT id FROM administration_lock WHERE id=1 FOR UPDATE"); e != nil {
		problem(c, e)
		return
	}
	var invite Invitation
	if e = tx.GetContext(c, &invite, invitationSelect+" AND i.token_hash=? FOR UPDATE", digest(c.Param("token"))); e != nil {
		if errors.Is(e, sql.ErrNoRows) {
			fail(c, 404, "Invitation is unavailable or expired")
		} else {
			problem(c, e)
		}
		return
	}
	if invite.Email != "" && invite.Email != email(in.Email) {
		fail(c, 400, "Use the email address specified in this invitation")
		return
	}
	if invite.SpaceID != nil {
		var personal bool
		if e = tx.GetContext(c, &personal, "SELECT personal FROM spaces WHERE id=? FOR UPDATE", *invite.SpaceID); e != nil || personal {
			fail(c, 409, "The invited shared space is no longer available")
			return
		}
	}
	id, e := a.createUser(c, tx, email(in.Email), strings.TrimSpace(in.Name), "", string(hash))
	if e != nil {
		var duplicate *mysql.MySQLError
		if errors.As(e, &duplicate) && duplicate.Number == 1062 {
			fail(c, 409, "This email already has an account. Sign in and ask the administrator to add you to the space.")
		} else {
			problem(c, e)
		}
		return
	}
	if invite.SpaceID != nil {
		if _, e = tx.ExecContext(c, "INSERT INTO members(space_id,user_id,role) VALUES(?,?,?)", *invite.SpaceID, id, invite.Role); e != nil {
			problem(c, e)
			return
		}
	}
	if _, e = tx.ExecContext(c, "UPDATE invitations SET used_at=UTC_TIMESTAMP(6) WHERE id=?", invite.ID); e != nil {
		problem(c, e)
		return
	}
	token := randomToken()
	if _, e = tx.ExecContext(c, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES(?,?,?)", digest(token), id, time.Now().UTC().Add(7*24*time.Hour)); e != nil {
		problem(c, e)
		return
	}
	if e = tx.Commit(); e != nil {
		problem(c, e)
		return
	}
	a.cookie(c, "md_session", token, 7*86400)
	c.Status(201)
}
