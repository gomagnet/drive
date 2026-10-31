package drive

import (
	"MagnetDrive/web"
	"github.com/gin-gonic/gin"
	"time"
)

type Space struct {
	ID        int64  `db:"id" json:"id"`
	Name      string `db:"name" json:"name"`
	OwnerName string `db:"owner_name" json:"owner_name"`
	OwnerID   int64  `db:"owner_id" json:"owner_id"`
	Personal  bool   `db:"personal" json:"personal"`
	Quota     int64  `db:"quota_bytes" json:"quota_bytes"`
	Used      int64  `db:"used_bytes" json:"used_bytes"`
	Role      string `db:"role" json:"role"`
	Joined    bool   `db:"is_member" json:"joined"`
}

const spaceAccessFrom = ` FROM spaces s JOIN users space_owner ON space_owner.id=s.owner_id JOIN users actor ON actor.id=? AND actor.deleted_at IS NULL LEFT JOIN members m ON m.space_id=s.id AND m.user_id=actor.id WHERE (actor.is_admin=TRUE OR m.user_id IS NOT NULL)`
const spaceColumns = `SELECT s.*,space_owner.name owner_name,(m.user_id IS NOT NULL) is_member,CASE WHEN actor.is_admin=TRUE THEN 'owner' ELSE m.role END role,`
const spaceStatSelect = spaceColumns + `0 used_bytes`
const spaceListSelect = spaceColumns + `COALESCE((SELECT SUM(v.size) FROM versions v JOIN entries e ON e.id=v.entry_id WHERE e.space_id=s.id),0) used_bytes`

func (a *App) spaces(c *gin.Context) {
	list := []Space{}
	e := a.db.SelectContext(c, &list, spaceListSelect+spaceAccessFrom+" ORDER BY (s.personal AND s.owner_id=actor.id) DESC,s.name,s.id", uid(c))
	if e != nil {
		problem(c, e)
		return
	}
	c.JSON(200, list)
}
func (a *App) createSpace(c *gin.Context) {
	var in struct {
		Name string `json:"name"`
	}
	if c.ShouldBindJSON(&in) != nil || !validName(in.Name) || len(in.Name) > 160 {
		fail(c, 400, "Enter a space name up to 160 bytes")
		return
	}
	tx, e := a.db.BeginTxx(c, nil)
	if e != nil {
		problem(c, e)
		return
	}
	defer tx.Rollback()
	res, e := tx.ExecContext(c, "INSERT INTO spaces(name,owner_id,quota_bytes) VALUES(?,?,?)", in.Name, uid(c), a.cfg.Quota)
	if e != nil {
		problem(c, e)
		return
	}
	id, e := res.LastInsertId()
	if e == nil {
		_, e = tx.ExecContext(c, "INSERT INTO members(space_id,user_id,role) VALUES(?,?,'owner')", id, uid(c))
	}
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		problem(c, e)
		return
	}
	c.JSON(201, gin.H{"id": id})
}
func (a *App) members(c *gin.Context) {
	id := number(c.Param("space"))
	if !a.access(c, id, false) {
		return
	}
	type member struct {
		User
		Role string `db:"role" json:"role"`
	}
	list := []member{}
	if e := a.db.SelectContext(c, &list, "SELECT u.id,u.email,u.name,u.avatar,m.role FROM members m JOIN users u ON u.id=m.user_id WHERE space_id=? ORDER BY u.name", id); e != nil {
		problem(c, e)
		return
	}
	c.JSON(200, list)
}
func (a *App) addMember(c *gin.Context) {
	var in struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if c.ShouldBindJSON(&in) != nil || email(in.Email) == "" || (in.Role != "editor" && in.Role != "viewer") {
		fail(c, 400, "Enter an existing account email and a valid role")
		return
	}
	space := number(c.Param("space"))
	tx, e := a.db.BeginTxx(c, nil)
	if e != nil {
		problem(c, e)
		return
	}
	defer tx.Rollback()
	if lockSpace(c, tx, space, uid(c), true) != nil {
		fail(c, 403, "Space unavailable")
		return
	}
	var owner int64
	var personal bool
	if e = tx.QueryRowxContext(c, "SELECT owner_id,personal FROM spaces WHERE id=?", space).Scan(&owner, &personal); e != nil {
		problem(c, e)
		return
	}
	if (owner != uid(c) && !c.MustGet("user").(User).Admin) || personal {
		fail(c, 403, "Only a shared space owner can manage members")
		return
	}
	var user int64
	if e = tx.GetContext(c, &user, "SELECT id FROM users WHERE email=? AND deleted_at IS NULL", email(in.Email)); e != nil {
		fail(c, 404, "No account with that email. Ask them to sign in first.")
		return
	}
	if user == owner {
		fail(c, 400, "The owner role cannot be changed")
		return
	}
	_, e = tx.ExecContext(c, "INSERT INTO members(space_id,user_id,role) VALUES(?,?,?) ON DUPLICATE KEY UPDATE role=VALUES(role)", space, user, in.Role)
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		problem(c, e)
		return
	}
	c.Status(204)
}
func (a *App) removeMember(c *gin.Context) {
	space := number(c.Param("space"))
	user := number(c.Param("user"))
	tx, e := a.db.BeginTxx(c, nil)
	if e != nil {
		problem(c, e)
		return
	}
	defer tx.Rollback()
	if lockSpace(c, tx, space, uid(c), true) != nil {
		fail(c, 403, "Space unavailable")
		return
	}
	var owner int64
	if e = tx.GetContext(c, &owner, "SELECT owner_id FROM spaces WHERE id=?", space); e != nil {
		problem(c, e)
		return
	}
	if (owner != uid(c) && !c.MustGet("user").(User).Admin) || user == owner {
		fail(c, 403, "Only the owner can remove other members")
		return
	}
	_, e = tx.ExecContext(c, "DELETE FROM members WHERE space_id=? AND user_id=?", space, user)
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		problem(c, e)
		return
	}
	c.Status(204)
}

type Share struct {
	ID      int64      `db:"id" json:"id"`
	Expires *time.Time `db:"expires_at" json:"expires_at"`
	Created time.Time  `db:"created_at" json:"created_at"`
}

func (a *App) shares(c *gin.Context) {
	ent, ok := a.entry(c, false)
	if !ok {
		return
	}
	list := []Share{}
	if e := a.db.SelectContext(c, &list, "SELECT id,expires_at,created_at FROM shares WHERE entry_id=? AND (expires_at IS NULL OR expires_at>UTC_TIMESTAMP(6))", ent.ID); e != nil {
		problem(c, e)
		return
	}
	c.JSON(200, list)
}
func (a *App) share(c *gin.Context) {
	ent, ok := a.entry(c, true)
	if !ok {
		return
	}
	if ent.Folder || ent.DeletedAt != nil {
		fail(c, 400, "Public links are available for active files")
		return
	}
	var in struct {
		Days   *int `json:"days"`
		Direct bool `json:"direct"`
	}
	if c.ShouldBindJSON(&in) != nil || in.Days == nil || *in.Days < 0 || *in.Days > 365 {
		fail(c, 400, "Choose forever (0) or an expiry between 1 and 365 days")
		return
	}
	token := randomToken()
	var expires *time.Time
	if *in.Days > 0 {
		v := time.Now().UTC().Add(time.Duration(*in.Days) * 24 * time.Hour)
		expires = &v
	}
	_, e := a.db.ExecContext(c, "INSERT INTO shares(token_hash,entry_id,created_by,expires_at) VALUES(?,?,?,?)", digest(token), ent.ID, uid(c), expires)
	if e != nil {
		problem(c, e)
		return
	}
	previewURL := a.cfg.PublicURL + "/s/" + token
	directURL := previewURL + "/content"
	link := previewURL
	if in.Direct {
		link = directURL
	}
	c.JSON(201, gin.H{"url": link, "preview_url": previewURL, "direct_url": directURL, "expires_at": expires})
}
func (a *App) revoke(c *gin.Context) {
	var space int64
	if e := a.db.GetContext(c, &space, "SELECT e.space_id FROM shares s JOIN entries e ON e.id=s.entry_id WHERE s.id=?", number(c.Param("id"))); e != nil {
		fail(c, 404, "Link unavailable")
		return
	}
	if !a.access(c, space, true) {
		return
	}
	if _, e := a.db.ExecContext(c, "DELETE FROM shares WHERE id=?", number(c.Param("id"))); e != nil {
		problem(c, e)
		return
	}
	c.Status(204)
}
func (a *App) publicEntry(c *gin.Context) (Entry, bool) {
	var ent Entry
	token := c.Param("token")
	if len(token) != 64 {
		fail(c, 404, "Link unavailable or expired")
		return ent, false
	}
	e := a.db.GetContext(c, &ent, entrySelect+"JOIN shares s ON s.entry_id=e.id WHERE s.token_hash=? AND (s.expires_at IS NULL OR s.expires_at>UTC_TIMESTAMP(6)) AND e.deleted_at IS NULL AND e.folder=FALSE", digest(token))
	if e != nil {
		fail(c, 404, "Link unavailable or expired")
		return ent, false
	}
	return ent, true
}
func (a *App) publicPage(c *gin.Context) {
	ent, ok := a.publicEntry(c)
	if !ok {
		return
	}
	if c.Query("meta") == "1" {
		c.JSON(200, ent)
		return
	}
	data, e := web.Files.ReadFile("index.html")
	if e != nil {
		problem(c, e)
		return
	}
	c.Data(200, "text/html; charset=utf-8", data)
}
func (a *App) publicContent(c *gin.Context) {
	ent, ok := a.publicEntry(c)
	if ok {
		a.serve(c, ent, ent.CurrentVersion)
	}
}
