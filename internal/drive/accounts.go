package drive

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
	"golang.org/x/crypto/bcrypt"
)

func validAvatar(v string) bool {
	if v == "" {
		return true
	}
	u, e := url.Parse(v)
	return e == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && len(v) <= 2048
}
func (a *App) updateProfile(c *gin.Context) {
	var in struct {
		Name     string  `json:"name"`
		Avatar   string  `json:"avatar"`
		Theme    string  `json:"theme"`
		Language *string `json:"language"`
	}
	if c.ShouldBindJSON(&in) != nil || strings.TrimSpace(in.Name) == "" || len(in.Name) > 160 || !validAvatar(in.Avatar) || (in.Theme != "auto" && in.Theme != "light" && in.Theme != "dark") {
		fail(c, 400, "Enter a name, HTTPS avatar URL (or leave empty), and a valid theme")
		return
	}
	if in.Language != nil && *in.Language != "auto" && *in.Language != "en" && *in.Language != "ru" {
		fail(c, 400, "Choose System, English, or Russian as your language")
		return
	}
	if _, e := a.db.ExecContext(c, "UPDATE users SET name=?,avatar=?,theme=?,language=COALESCE(?,language) WHERE id=? AND deleted_at IS NULL", strings.TrimSpace(in.Name), in.Avatar, in.Theme, in.Language, uid(c)); e != nil {
		problem(c, e)
		return
	}
	c.Status(204)
}
func (a *App) updatePassword(c *gin.Context) {
	if !a.authSlot(c) {
		return
	}
	defer func() { <-a.authSlots }()
	var in struct {
		Current  string `json:"current"`
		Password string `json:"password"`
	}
	if c.ShouldBindJSON(&in) != nil || len(in.Password) < 12 || len(in.Password) > 72 || len(in.Current) > 72 {
		fail(c, 400, "Use a password of 12–72 bytes")
		return
	}
	tx, e := a.db.BeginTxx(c, nil)
	if e != nil {
		problem(c, e)
		return
	}
	defer tx.Rollback()
	var old string
	if e = tx.GetContext(c, &old, "SELECT password_hash FROM users WHERE id=? AND deleted_at IS NULL FOR UPDATE", uid(c)); e != nil {
		problem(c, e)
		return
	}
	if old != "" && bcrypt.CompareHashAndPassword([]byte(old), []byte(in.Current)) != nil {
		fail(c, 403, "Current password is incorrect")
		return
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(in.Password), 12)
	if e != nil {
		problem(c, e)
		return
	}
	if _, e = tx.ExecContext(c, "UPDATE users SET password_hash=? WHERE id=?", string(hash), uid(c)); e != nil {
		problem(c, e)
		return
	}
	token, _ := c.Cookie("md_session")
	if _, e = tx.ExecContext(c, "DELETE FROM sessions WHERE user_id=? AND token_hash<>?", uid(c), digest(token)); e != nil {
		problem(c, e)
		return
	}
	if e = tx.Commit(); e != nil {
		problem(c, e)
		return
	}
	c.Status(204)
}
func (a *App) requireAdmin(c *gin.Context) {
	if !c.MustGet("user").(User).Admin {
		fail(c, 403, "Administrator access required")
		return
	}
	c.Next()
}

// Serialize role/deletion changes so two concurrent requests cannot remove the
// final administrator. Check the actor again after acquiring the database lock.
func (a *App) adminTx(c *gin.Context) (*sqlx.Tx, error) {
	tx, e := a.db.BeginTxx(c, nil)
	if e != nil {
		return nil, e
	}
	var id int
	if e = tx.GetContext(c, &id, "SELECT id FROM administration_lock WHERE id=1 FOR UPDATE"); e == nil {
		var admin bool
		e = tx.GetContext(c, &admin, "SELECT is_admin FROM users WHERE id=? AND deleted_at IS NULL", uid(c))
		if e == nil && !admin {
			e = errors.New("administrator role changed; sign in again")
		}
	}
	if e != nil {
		tx.Rollback()
		return nil, e
	}
	return tx, nil
}

type adminUser struct {
	User
	Quota   int64 `db:"quota_bytes" json:"quota_bytes"`
	Used    int64 `db:"used_bytes" json:"used_bytes"`
	SpaceID int64 `db:"space_id" json:"space_id"`
}

func (a *App) adminUsers(c *gin.Context) {
	list := []adminUser{}
	e := a.db.SelectContext(c, &list, `SELECT u.id,u.email,u.name,u.avatar,u.is_admin,u.theme,u.language,s.id space_id,s.quota_bytes,COALESCE((SELECT SUM(v.size) FROM versions v JOIN entries e ON e.id=v.entry_id WHERE e.space_id=s.id),0) used_bytes FROM users u JOIN spaces s ON s.owner_id=u.id AND s.personal=TRUE WHERE u.deleted_at IS NULL ORDER BY u.name,u.id`)
	if e != nil {
		problem(c, e)
		return
	}
	c.JSON(200, list)
}

const maxQuota int64 = 9007199254740991

func (a *App) adminCreateUser(c *gin.Context) {
	if !a.authSlot(c) {
		return
	}
	defer func() { <-a.authSlots }()
	var in struct {
		credentials
		Admin bool   `json:"admin"`
		Quota *int64 `json:"quota_bytes"`
	}
	if c.ShouldBindJSON(&in) != nil || email(in.Email) == "" || strings.TrimSpace(in.Name) == "" || len(in.Name) > 160 || len(in.Password) < 12 || len(in.Password) > 72 || in.Quota != nil && (*in.Quota < 0 || *in.Quota > maxQuota) {
		fail(c, 400, "Enter a valid name, email, password (12–72 bytes), and storage limit")
		return
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(in.Password), 12)
	if e != nil {
		problem(c, e)
		return
	}
	tx, e := a.adminTx(c)
	if e != nil {
		problem(c, e)
		return
	}
	defer tx.Rollback()
	id, e := a.createUser(c, tx, email(in.Email), strings.TrimSpace(in.Name), "", string(hash))
	if e != nil {
		fail(c, 409, "An account with that email may already exist")
		return
	}
	if _, e = tx.ExecContext(c, "UPDATE users SET is_admin=? WHERE id=?", in.Admin, id); e != nil {
		problem(c, e)
		return
	}
	if in.Quota != nil {
		if _, e = tx.ExecContext(c, "UPDATE spaces SET quota_bytes=? WHERE owner_id=? AND personal=TRUE", *in.Quota, id); e != nil {
			problem(c, e)
			return
		}
	}
	if e = tx.Commit(); e != nil {
		problem(c, e)
		return
	}
	c.JSON(201, gin.H{"id": id})
}
func (a *App) adminUpdateUser(c *gin.Context) {
	var in struct {
		Admin *bool `json:"admin"`
	}
	if c.ShouldBindJSON(&in) != nil || in.Admin == nil {
		fail(c, 400, "Invalid role")
		return
	}
	id := number(c.Param("id"))
	tx, e := a.adminTx(c)
	if e != nil {
		problem(c, e)
		return
	}
	defer tx.Rollback()
	var current bool
	if e = tx.GetContext(c, &current, "SELECT is_admin FROM users WHERE id=? AND deleted_at IS NULL FOR UPDATE", id); e != nil {
		fail(c, 404, "User not found")
		return
	}
	if current && !*in.Admin {
		var count int
		if e = tx.GetContext(c, &count, "SELECT COUNT(*) FROM users WHERE is_admin=TRUE AND deleted_at IS NULL"); e != nil {
			problem(c, e)
			return
		}
		if count <= 1 {
			fail(c, 409, "The last administrator cannot be demoted")
			return
		}
		if _, e = tx.ExecContext(c, "DELETE FROM invitations WHERE created_by=?", id); e != nil {
			problem(c, e)
			return
		}
	}
	if _, e = tx.ExecContext(c, "UPDATE users SET is_admin=? WHERE id=?", *in.Admin, id); e == nil {
		e = tx.Commit()
	}
	if e != nil {
		problem(c, e)
		return
	}
	c.Status(204)
}
func (a *App) adminQuota(c *gin.Context) {
	var in struct {
		Quota *int64 `json:"quota_bytes"`
	}
	if c.ShouldBindJSON(&in) != nil || in.Quota == nil || *in.Quota < 0 || *in.Quota > maxQuota {
		fail(c, 400, "Enter a storage limit between 0 and 9007199254740991 bytes")
		return
	}
	tx, e := a.adminTx(c)
	if e != nil {
		problem(c, e)
		return
	}
	defer tx.Rollback()
	var id int64
	if e = tx.GetContext(c, &id, "SELECT id FROM spaces WHERE id=? FOR UPDATE", number(c.Param("id"))); e != nil {
		fail(c, 404, "Space not found")
		return
	}
	if _, e = tx.ExecContext(c, "UPDATE spaces SET quota_bytes=? WHERE id=?", *in.Quota, id); e == nil {
		e = tx.Commit()
	}
	if e != nil {
		problem(c, e)
		return
	}
	c.Status(204)
}
func (a *App) adminDeleteUser(c *gin.Context) {
	id := number(c.Param("id"))
	if id == uid(c) {
		fail(c, 409, "You cannot delete your own account")
		return
	}
	tx, e := a.adminTx(c)
	if e != nil {
		problem(c, e)
		return
	}
	defer tx.Rollback()
	var spaces []int64
	if e = tx.SelectContext(c, &spaces, "SELECT id FROM spaces ORDER BY id FOR UPDATE"); e != nil {
		problem(c, e)
		return
	}
	var target User
	if e = tx.GetContext(c, &target, "SELECT id,email,name,avatar,is_admin,theme,language FROM users WHERE id=? AND deleted_at IS NULL FOR UPDATE", id); e != nil {
		if errors.Is(e, sql.ErrNoRows) {
			fail(c, 404, "User not found")
		} else {
			problem(c, e)
		}
		return
	}
	// Keep audit references valid while removing identity and every access path.
	for _, q := range []string{"DELETE FROM invitations WHERE created_by=?", "DELETE FROM sessions WHERE user_id=?", "DELETE FROM identities WHERE user_id=?", "DELETE FROM sftp_credentials WHERE user_id=?", "DELETE FROM members WHERE user_id=?", "DELETE FROM shares WHERE created_by=?"} {
		if _, e = tx.ExecContext(c, q, id); e != nil {
			problem(c, e)
			return
		}
	}
	if _, e = tx.ExecContext(c, "UPDATE spaces SET name=IF(personal=TRUE,CONCAT(LEFT(?,130),' (archived personal)'),name),personal=FALSE,owner_id=? WHERE owner_id=?", target.Name, uid(c), id); e != nil {
		problem(c, e)
		return
	}
	for _, space := range spaces {
		var owner int64
		if e = tx.GetContext(c, &owner, "SELECT owner_id FROM spaces WHERE id=?", space); e != nil {
			problem(c, e)
			return
		}
		if owner == uid(c) {
			if _, e = tx.ExecContext(c, "INSERT INTO members(space_id,user_id,role) VALUES(?,?,'owner') ON DUPLICATE KEY UPDATE role='owner'", space, uid(c)); e != nil {
				problem(c, e)
				return
			}
		}
	}
	if _, e = tx.ExecContext(c, "UPDATE users SET email=?,name='Deleted user',avatar='',password_hash='',is_admin=FALSE,deleted_at=UTC_TIMESTAMP(6) WHERE id=?", fmt.Sprintf("deleted-%d-%s@invalid.local", id, randomToken()), id); e == nil {
		e = tx.Commit()
	}
	if e != nil {
		problem(c, e)
		return
	}
	c.Status(204)
}
