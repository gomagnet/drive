package drive

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
)

type Entry struct {
	ID             int64      `db:"id" json:"id"`
	SpaceID        int64      `db:"space_id" json:"space_id"`
	ParentID       *int64     `db:"parent_id" json:"parent_id"`
	Name           string     `db:"name" json:"name"`
	Folder         bool       `db:"folder" json:"folder"`
	CurrentVersion *int64     `db:"current_version" json:"current_version"`
	CreatedBy      int64      `db:"created_by" json:"created_by"`
	EditedBy       int64      `db:"edited_by" json:"edited_by"`
	CreatedAt      time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt      time.Time  `db:"updated_at" json:"updated_at"`
	DeletedAt      *time.Time `db:"deleted_at" json:"deleted_at"`
	Size           int64      `db:"size" json:"size"`
	MIME           string     `db:"mime" json:"mime"`
	Creator        string     `db:"creator" json:"creator"`
	Editor         string     `db:"editor" json:"editor"`
}

const entrySelect = "SELECT e.*,COALESCE(v.size,0) size,COALESCE(v.mime,'') mime,u.name creator,ed.name editor FROM entries e LEFT JOIN versions v ON v.id=e.current_version JOIN users u ON u.id=e.created_by JOIN users ed ON ed.id=e.edited_by "

func number(s string) int64 { n, _ := strconv.ParseInt(s, 10, 64); return n }
func validName(s string) bool {
	return utf8.ValidString(s) && len(s) > 0 && len(s) <= 255 && s == strings.TrimSpace(s) && s != "." && s != ".." && !strings.ContainsAny(s, "/\\\x00\r\n")
}
func (a *App) access(c *gin.Context, space int64, write bool) bool {
	if e := checkAPIKey(c, a.db, space, write); e != nil {
		fail(c, 403, "API key does not permit access to this space")
		return false
	}
	if c.MustGet("user").(User).Admin {
		var id int64
		if e := a.db.GetContext(c, &id, "SELECT id FROM spaces WHERE id=?", space); e != nil {
			fail(c, 404, "Space not found")
			return false
		}
		return true
	}
	var role string
	e := a.db.GetContext(c, &role, "SELECT role FROM members WHERE space_id=? AND user_id=?", space, uid(c))
	if e != nil || write && role == "viewer" {
		fail(c, 403, "You do not have access to this space")
		return false
	}
	return true
}
func (a *App) entry(c *gin.Context, write bool) (Entry, bool) {
	var e Entry
	err := a.db.GetContext(c, &e, entrySelect+"WHERE e.id=?", number(c.Param("id")))
	if err != nil {
		fail(c, 404, "File not found")
		return e, false
	}
	return e, a.access(c, e.SpaceID, write)
}

// Space row locks serialize tree mutations, quota reservations, and membership changes.
func lockSpace(c context.Context, tx *sqlx.Tx, space, user int64, write bool) error {
	var id int64
	if e := tx.GetContext(c, &id, "SELECT id FROM spaces WHERE id=? FOR UPDATE", space); e != nil {
		return e
	}
	if e := checkAPIKey(c, tx, space, write); e != nil {
		return e
	}
	var admin bool
	if e := tx.GetContext(c, &admin, "SELECT is_admin FROM users WHERE id=? AND deleted_at IS NULL", user); e != nil {
		return e
	}
	if admin {
		return nil
	}
	var role string
	if e := tx.GetContext(c, &role, "SELECT role FROM members WHERE space_id=? AND user_id=?", space, user); e != nil {
		return e
	}
	if write && role == "viewer" {
		return errors.New("read-only space")
	}
	return nil
}
func parentOK(c context.Context, tx *sqlx.Tx, space int64, parent *int64) bool {
	if parent == nil {
		return true
	}
	var n int
	return tx.GetContext(c, &n, "SELECT COUNT(*) FROM entries WHERE id=? AND space_id=? AND folder=TRUE AND deleted_at IS NULL", *parent, space) == nil && n == 1
}
func uniqueName(c context.Context, tx *sqlx.Tx, space int64, parent *int64, name string, exclude int64) bool {
	var n int
	return tx.GetContext(c, &n, "SELECT COUNT(*) FROM entries WHERE space_id=? AND parent_id <=> ? AND name=? AND deleted_at IS NULL AND id<>?", space, parent, name, exclude) == nil && n == 0
}
func (a *App) entries(c *gin.Context) {
	space := number(c.Query("space"))
	if !a.access(c, space, false) {
		return
	}
	q := entrySelect + "WHERE e.space_id=? "
	args := []any{space}
	if c.Query("trash") == "1" {
		q += "AND e.deleted_at IS NOT NULL "
	} else {
		q += "AND e.deleted_at IS NULL "
		if c.Query("q") == "" && c.Query("recent") != "1" {
			q += "AND e.parent_id <=> ? "
			var parent *int64
			if n := number(c.Query("parent")); n > 0 {
				parent = &n
			}
			args = append(args, parent)
		}
	}
	if search := c.Query("q"); search != "" {
		q += "AND LOCATE(?,e.name)>0 "
		args = append(args, search)
	}
	limit := 1000
	versioned := c.Value(apiKeyIDContext) != nil
	if versioned {
		limit = 200
		if value := c.Query("limit"); value != "" {
			n, e := strconv.Atoi(value)
			if e != nil || n < 1 || n > 1000 {
				fail(c, 400, "Use a limit of 1–1000 and a non-negative after cursor")
				return
			}
			limit = n
		}
		after := int64(0)
		if value := c.Query("after"); value != "" {
			n, e := strconv.ParseInt(value, 10, 64)
			if e != nil || n < 0 {
				fail(c, 400, "Use a limit of 1–1000 and a non-negative after cursor")
				return
			}
			after = n
		}
		q += "AND e.id>? ORDER BY e.id ASC LIMIT ?"
		args = append(args, after, limit+1)
	} else {
		q += "ORDER BY e.folder DESC,e.updated_at DESC LIMIT 1000"
	}
	list := []Entry{}
	if e := a.db.SelectContext(c, &list, q, args...); e != nil {
		problem(c, e)
		return
	}
	if versioned {
		next := ""
		if len(list) > limit {
			list = list[:limit]
			next = strconv.FormatInt(list[len(list)-1].ID, 10)
		}
		c.JSON(200, gin.H{"entries": list, "limit": limit, "next_cursor": next})
		return
	}
	c.JSON(200, gin.H{"entries": list, "limit": limit})
}
func (a *App) folder(c *gin.Context) {
	var in struct {
		Space  int64  `json:"space"`
		Parent *int64 `json:"parent"`
		Name   string `json:"name"`
	}
	if c.ShouldBindJSON(&in) != nil || !validName(in.Name) {
		fail(c, 400, "Enter a valid folder name")
		return
	}
	tx, e := a.db.BeginTxx(c, nil)
	if e != nil {
		problem(c, e)
		return
	}
	defer tx.Rollback()
	if lockSpace(c, tx, in.Space, uid(c), true) != nil {
		fail(c, 403, "Space is read-only")
		return
	}
	if !parentOK(c, tx, in.Space, in.Parent) || !uniqueName(c, tx, in.Space, in.Parent, in.Name, 0) {
		fail(c, 409, "Folder unavailable or name already exists")
		return
	}
	res, e := tx.ExecContext(c, "INSERT INTO entries(space_id,parent_id,name,folder,created_by,edited_by) VALUES(?,?,?,TRUE,?,?)", in.Space, in.Parent, in.Name, uid(c), uid(c))
	var id int64
	if e == nil {
		id, e = res.LastInsertId()
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
func (a *App) upload(c *gin.Context) {
	if !a.access(c, number(c.Query("space")), true) {
		return
	}
	select {
	case a.uploadSlots <- struct{}{}:
		defer func() { <-a.uploadSlots }()
	default:
		fail(c, 429, "Uploads are busy. Please try again shortly.")
		return
	}
	// Stream directly to a random immutable object; client paths never reach the filesystem.
	reader, e := c.Request.MultipartReader()
	if e != nil {
		fail(c, 400, "Expected a file upload")
		return
	}
	part, e := reader.NextPart()
	if e != nil || part.FormName() != "file" {
		fail(c, 400, "Expected file as the first part")
		return
	}
	defer part.Close()
	name := part.FileName()
	if !validName(name) {
		fail(c, 400, "Invalid file name")
		return
	}
	key := randomToken()
	path := filepath.Join(a.cfg.DataDir, key)
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		problem(c, e)
		return
	}
	committed := false
	defer func() {
		f.Close()
		if !committed {
			_ = os.Remove(path)
		}
	}()
	head := make([]byte, 512)
	n, e := io.ReadFull(part, head)
	if e != nil && e != io.EOF && e != io.ErrUnexpectedEOF {
		fail(c, 400, "Upload interrupted")
		return
	}
	head = head[:n]
	kind := contentType(name, head)
	// Text formats remain plain text; HTML and SVG are always served as downloads.
	size, e := io.Copy(f, io.LimitReader(io.MultiReader(strings.NewReader(string(head)), part), a.cfg.MaxUpload+1))
	if e != nil || size > a.cfg.MaxUpload {
		fail(c, 413, "File exceeds the upload limit or was interrupted")
		return
	}
	if e = f.Sync(); e != nil {
		problem(c, e)
		return
	}
	if e = f.Close(); e != nil {
		problem(c, e)
		return
	}
	space := number(c.Query("space"))
	var parent *int64
	if id := number(c.Query("parent")); id > 0 {
		parent = &id
	}
	tx, e := a.db.BeginTxx(c, nil)
	if e != nil {
		problem(c, e)
		return
	}
	defer tx.Rollback()
	if lockSpace(c, tx, space, uid(c), true) != nil {
		fail(c, 403, "Space is read-only")
		return
	}
	var quota, used int64
	if e = tx.GetContext(c, &quota, "SELECT quota_bytes FROM spaces WHERE id=?", space); e != nil {
		problem(c, e)
		return
	}
	if e = tx.GetContext(c, &used, "SELECT COALESCE(SUM(v.size),0) FROM versions v JOIN entries e ON e.id=v.entry_id WHERE e.space_id=?", space); e != nil {
		problem(c, e)
		return
	}
	if size > quota-used {
		fail(c, 413, "This space is full. All file versions count toward storage.")
		return
	}
	id := number(c.Query("replace"))
	if id > 0 {
		var old Entry
		e = tx.GetContext(c, &old, entrySelect+"WHERE e.id=? AND e.space_id=? AND e.folder=FALSE AND e.deleted_at IS NULL", id, space)
		if e != nil {
			fail(c, 404, "Original file unavailable")
			return
		}
	} else {
		if !parentOK(c, tx, space, parent) || !uniqueName(c, tx, space, parent, name, 0) {
			fail(c, 409, "Folder unavailable or name exists. Use Upload new version to replace it.")
			return
		}
		res, err := tx.ExecContext(c, "INSERT INTO entries(space_id,parent_id,name,created_by,edited_by) VALUES(?,?,?,?,?)", space, parent, name, uid(c), uid(c))
		if err != nil {
			problem(c, err)
			return
		}
		id, e = res.LastInsertId()
		if e != nil {
			problem(c, e)
			return
		}
	}
	res, e := tx.ExecContext(c, "INSERT INTO versions(entry_id,blob_key,size,mime,created_by) VALUES(?,?,?,?,?)", id, key, size, kind, uid(c))
	if e != nil {
		problem(c, e)
		return
	}
	version, e := res.LastInsertId()
	if e == nil {
		_, e = tx.ExecContext(c, "UPDATE entries SET current_version=?,edited_by=?,updated_at=UTC_TIMESTAMP(6) WHERE id=?", version, uid(c), id)
	}
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		problem(c, e)
		return
	}
	committed = true
	c.JSON(201, gin.H{"id": id})
}
func (a *App) edit(c *gin.Context) {
	entry, ok := a.entry(c, true)
	if !ok {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if c.ShouldBindJSON(&in) != nil || !validName(in.Name) {
		fail(c, 400, "Invalid file name")
		return
	}
	tx, e := a.db.BeginTxx(c, nil)
	if e != nil {
		problem(c, e)
		return
	}
	defer tx.Rollback()
	if lockSpace(c, tx, entry.SpaceID, uid(c), true) != nil {
		fail(c, 403, "Space is read-only")
		return
	}
	if e = tx.GetContext(c, &entry, entrySelect+"WHERE e.id=? AND e.deleted_at IS NULL", entry.ID); e != nil {
		fail(c, 404, "File unavailable")
		return
	}
	if !uniqueName(c, tx, entry.SpaceID, entry.ParentID, in.Name, entry.ID) {
		fail(c, 409, "That name already exists")
		return
	}
	_, e = tx.ExecContext(c, "UPDATE entries SET name=?,edited_by=?,updated_at=UTC_TIMESTAMP(6) WHERE id=? AND deleted_at IS NULL", in.Name, uid(c), entry.ID)
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		problem(c, e)
		return
	}
	c.Status(204)
}
func (a *App) bulk(c *gin.Context) {
	var in struct {
		Space  int64   `json:"space"`
		IDs    []int64 `json:"ids"`
		Action string  `json:"action"`
		Parent *int64  `json:"parent"`
	}
	if c.ShouldBindJSON(&in) != nil || len(in.IDs) < 1 || len(in.IDs) > 200 || (in.Action != "trash" && in.Action != "restore" && in.Action != "move") {
		fail(c, 400, "Invalid bulk operation")
		return
	}
	tx, e := a.db.BeginTxx(c, nil)
	if e != nil {
		problem(c, e)
		return
	}
	defer tx.Rollback()
	if lockSpace(c, tx, in.Space, uid(c), true) != nil {
		fail(c, 403, "Space is read-only")
		return
	}
	if in.Action == "move" && !parentOK(c, tx, in.Space, in.Parent) {
		fail(c, 400, "Destination folder unavailable")
		return
	}
	selected := map[int64]bool{}
	for _, id := range in.IDs {
		selected[id] = true
	}
	for _, id := range in.IDs {
		var ent Entry
		if e = tx.GetContext(c, &ent, entrySelect+"WHERE e.id=? AND e.space_id=?", id, in.Space); e != nil {
			fail(c, 404, "A selected file is unavailable")
			return
		}
		// Skip descendants of a selected ancestor so folder operations stay atomic.
		skip := false
		p := ent.ParentID
		for depth := 0; p != nil; depth++ {
			if depth > 256 {
				fail(c, 400, "Folder nesting is too deep")
				return
			}
			if selected[*p] {
				skip = true
				break
			}
			var row struct {
				Parent *int64 `db:"parent_id"`
			}
			if e = tx.GetContext(c, &row, "SELECT parent_id FROM entries WHERE id=?", *p); e != nil {
				problem(c, e)
				return
			}
			p = row.Parent
		}
		if skip {
			continue
		}
		if in.Action == "move" {
			p = in.Parent
			for depth := 0; p != nil; depth++ {
				if *p == id || depth > 256 {
					fail(c, 400, "Cannot move a folder into itself")
					return
				}
				var row struct {
					Parent *int64 `db:"parent_id"`
				}
				if e = tx.GetContext(c, &row, "SELECT parent_id FROM entries WHERE id=? AND space_id=?", *p, in.Space); e != nil {
					problem(c, e)
					return
				}
				p = row.Parent
			}
			if ent.DeletedAt != nil || !uniqueName(c, tx, in.Space, in.Parent, ent.Name, id) {
				fail(c, 409, "Destination already contains this name")
				return
			}
			_, e = tx.ExecContext(c, "UPDATE entries SET parent_id=?,edited_by=?,updated_at=UTC_TIMESTAMP(6) WHERE id=?", in.Parent, uid(c), id)
		} else {
			if in.Action == "restore" {
				if !parentOK(c, tx, in.Space, ent.ParentID) || !uniqueName(c, tx, in.Space, ent.ParentID, ent.Name, id) {
					fail(c, 409, "Restore the parent folder first or resolve the name conflict")
					return
				}
				_, e = tx.ExecContext(c, "UPDATE entries SET deleted_at=NULL,edited_by=?,updated_at=UTC_TIMESTAMP(6) WHERE id=?", uid(c), id)
			} else {
				_, e = tx.ExecContext(c, "UPDATE entries SET deleted_at=UTC_TIMESTAMP(6),edited_by=?,updated_at=UTC_TIMESTAMP(6) WHERE id=?", uid(c), id)
			}
			// Apply trash/restore to descendants. Space locking prevents concurrent tree changes.
			if e == nil && ent.Folder {
				queue := []int64{id}
				for len(queue) > 0 {
					next := queue[0]
					queue = queue[1:]
					var children []int64
					if e = tx.SelectContext(c, &children, "SELECT id FROM entries WHERE parent_id=? AND space_id=?", next, in.Space); e != nil {
						break
					}
					queue = append(queue, children...)
					for _, child := range children {
						if in.Action == "trash" {
							_, e = tx.ExecContext(c, "UPDATE entries SET deleted_at=COALESCE(deleted_at,UTC_TIMESTAMP(6)),edited_by=? WHERE id=?", uid(c), child)
						} /* Restore children individually from Trash to preserve independently trashed items. */
						if e != nil {
							break
						}
					}
					if e != nil {
						break
					}
				}
			}
		}
		if e != nil {
			problem(c, e)
			return
		}
	}
	if e = tx.Commit(); e != nil {
		problem(c, e)
		return
	}
	c.Status(204)
}

type Version struct {
	ID        int64     `db:"id" json:"id"`
	EntryID   int64     `db:"entry_id" json:"entry_id"`
	Key       string    `db:"blob_key" json:"-"`
	Size      int64     `db:"size" json:"size"`
	MIME      string    `db:"mime" json:"mime"`
	CreatedBy int64     `db:"created_by" json:"created_by"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
	Creator   string    `db:"creator" json:"creator"`
}

func (a *App) versions(c *gin.Context) {
	entry, ok := a.entry(c, false)
	if !ok {
		return
	}
	list := []Version{}
	if e := a.db.SelectContext(c, &list, "SELECT v.*,u.name creator FROM versions v JOIN users u ON u.id=v.created_by WHERE entry_id=? ORDER BY v.id DESC", entry.ID); e != nil {
		problem(c, e)
		return
	}
	c.JSON(200, list)
}
func (a *App) rollback(c *gin.Context) {
	ent, ok := a.entry(c, true)
	if !ok {
		return
	}
	var in struct {
		Version int64 `json:"version"`
	}
	if c.ShouldBindJSON(&in) != nil {
		fail(c, 400, "Invalid version")
		return
	}
	tx, e := a.db.BeginTxx(c, nil)
	if e != nil {
		problem(c, e)
		return
	}
	defer tx.Rollback()
	if lockSpace(c, tx, ent.SpaceID, uid(c), true) != nil {
		fail(c, 403, "Space is read-only")
		return
	}
	var v Version
	e = tx.GetContext(c, &v, "SELECT v.*,u.name creator FROM versions v JOIN users u ON u.id=v.created_by WHERE v.id=? AND entry_id=?", in.Version, ent.ID)
	if e != nil {
		fail(c, 404, "Version unavailable")
		return
	}
	_, e = tx.ExecContext(c, "UPDATE entries SET current_version=?,edited_by=?,updated_at=UTC_TIMESTAMP(6) WHERE id=? AND deleted_at IS NULL", v.ID, uid(c), ent.ID)
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		problem(c, e)
		return
	}
	c.Status(204)
}
func (a *App) content(c *gin.Context) {
	ent, ok := a.entry(c, false)
	if !ok {
		return
	}
	if ent.DeletedAt != nil || ent.Folder {
		fail(c, 404, "File unavailable")
		return
	}
	v := ent.CurrentVersion
	if n := number(c.Query("version")); n > 0 {
		v = &n
	}
	a.serve(c, ent, v)
}
func (a *App) serve(c *gin.Context, ent Entry, version *int64) {
	var v Version
	if e := a.db.GetContext(c, &v, "SELECT v.*,u.name creator FROM versions v JOIN users u ON u.id=v.created_by WHERE v.id=? AND entry_id=?", version, ent.ID); e != nil {
		fail(c, 404, "File unavailable")
		return
	}
	if len(v.Key) != 64 || strings.ContainsAny(v.Key, "/\\.") {
		problem(c, errors.New("invalid object key"))
		return
	}
	f, e := os.Open(filepath.Join(a.cfg.DataDir, v.Key))
	if e != nil {
		fail(c, 404, "Stored content unavailable")
		return
	}
	defer f.Close()
	disposition := "attachment"
	if c.Query("download") != "1" && (strings.HasPrefix(v.MIME, "image/") && v.MIME != "image/svg+xml" || strings.HasPrefix(v.MIME, "audio/") || strings.HasPrefix(v.MIME, "video/") || strings.HasPrefix(v.MIME, "text/plain") || v.MIME == "application/pdf") {
		disposition = "inline"
	}
	c.Header("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": ent.Name}))
	c.Header("Content-Type", v.MIME)
	policy := "sandbox; default-src 'none'; style-src 'unsafe-inline'; media-src 'self'; img-src 'self'; frame-ancestors 'self'"
	if v.MIME == "application/pdf" {
		policy = "default-src 'none'; frame-ancestors 'self'"
	}
	c.Header("Content-Security-Policy", policy)
	http.ServeContent(c.Writer, c.Request, ent.Name, v.CreatedAt, f)
}

func contentType(name string, head []byte) string {
	kind := http.DetectContentType(head)
	if kind == "application/ogg" {
		kind = "audio/ogg"
		if strings.EqualFold(filepath.Ext(name), ".ogv") {
			kind = "video/ogg"
		}
	}
	if kind == "application/octet-stream" {
		candidate := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
		if known := map[string]string{".mp3": "audio/mpeg", ".ogg": "audio/ogg", ".oga": "audio/ogg", ".opus": "audio/ogg", ".flac": "audio/flac", ".wav": "audio/wav", ".aac": "audio/aac", ".m4a": "audio/mp4", ".mp4": "video/mp4", ".webm": "video/webm", ".ogv": "video/ogg"}[strings.ToLower(filepath.Ext(name))]; known != "" {
			candidate = known
		}
		if strings.HasPrefix(candidate, "audio/") || strings.HasPrefix(candidate, "video/") {
			kind = candidate
		}
	}
	return kind
}
