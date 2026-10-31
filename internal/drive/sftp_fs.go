package drive

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/jmoiron/sqlx"
	"github.com/pkg/sftp"
)

type sftpFS struct {
	app              *App
	user, credential int64
	passwordHash     string
	writesMu         sync.Mutex
	writes           map[string]*sftpWriteHandle
}
type sftpQuery interface {
	GetContext(context.Context, any, string, ...any) error
	SelectContext(context.Context, any, string, ...any) error
}
type sftpNode struct {
	root, virtual bool
	space         Space
	entry         *Entry
	parent        *int64
	name          string
}

func (h *sftpFS) spacePath(s Space) string {
	if s.Personal {
		if s.OwnerID == h.user {
			return "self"
		}
		name := strings.Map(func(r rune) rune {
			if r == '/' || r == '\\' || unicode.IsControl(r) {
				return '_'
			}
			return r
		}, s.OwnerName)
		return "personal-" + strconv.FormatInt(s.ID, 10) + " - " + name
	}
	return "shared-" + strconv.FormatInt(s.ID, 10) + " - " + s.Name
}
func (h *sftpFS) authenticated(ctx context.Context, q sftpQuery) error {
	var hash string
	if e := q.GetContext(ctx, &hash, "SELECT password_hash FROM users WHERE id=? AND deleted_at IS NULL", h.user); e != nil {
		return os.ErrPermission
	}
	if h.credential > 0 {
		var n int
		if e := q.GetContext(ctx, &n, "SELECT COUNT(*) FROM sftp_credentials WHERE id=? AND user_id=?", h.credential, h.user); e != nil || n != 1 {
			return os.ErrPermission
		}
	} else if hash == "" || digest(hash) != h.passwordHash {
		return os.ErrPermission
	}
	return nil
}
func (h *sftpFS) resolve(ctx context.Context, q sftpQuery, raw string, missing bool) (sftpNode, error) {
	var node sftpNode
	if e := h.authenticated(ctx, q); e != nil {
		return node, e
	}
	if strings.ContainsAny(raw, "\x00\\") || len(raw) > 65536 {
		return node, os.ErrInvalid
	}
	clean := path.Clean("/" + raw)
	if clean == "/" {
		node.root = true
		return node, nil
	}
	parts := strings.Split(strings.TrimPrefix(clean, "/"), "/")
	if len(parts) > 258 {
		return node, os.ErrInvalid
	}
	spaces := []Space{}
	if e := q.SelectContext(ctx, &spaces, spaceStatSelect+spaceAccessFrom, h.user); e != nil {
		return node, e
	}
	found := false
	for _, s := range spaces {
		if h.spacePath(s) == parts[0] {
			node.space = s
			node.name = parts[0]
			node.virtual = true
			found = true
			break
		}
	}
	if !found {
		return node, os.ErrNotExist
	}
	for i, name := range parts[1:] {
		node.virtual = false
		if !validName(name) {
			return node, os.ErrInvalid
		}
		var parent *int64
		if node.entry != nil {
			if !node.entry.Folder {
				return node, os.ErrNotExist
			}
			id := node.entry.ID
			parent = &id
		}
		var ent Entry
		e := q.GetContext(ctx, &ent, entrySelect+"WHERE e.space_id=? AND e.parent_id <=> ? AND e.name=? AND e.deleted_at IS NULL", node.space.ID, parent, name)
		if e != nil {
			if missing && i == len(parts)-2 && errors.Is(e, sql.ErrNoRows) {
				node.entry = nil
				node.parent = parent
				node.name = name
				return node, nil
			}
			return node, e
		}
		node.entry = &ent
		node.parent = parent
		node.name = ent.Name
	}
	return node, nil
}
func sftpError(e error) error {
	if e == nil {
		return nil
	}
	if errors.Is(e, sftp.ErrSSHFxOpUnsupported) {
		return sftp.ErrSSHFxOpUnsupported
	}
	if errors.Is(e, sql.ErrNoRows) || errors.Is(e, os.ErrNotExist) {
		return os.ErrNotExist
	}
	if errors.Is(e, os.ErrPermission) {
		return os.ErrPermission
	}
	if errors.Is(e, os.ErrExist) {
		return os.ErrExist
	}
	if errors.Is(e, os.ErrInvalid) {
		return os.ErrInvalid
	}
	return sftp.ErrSSHFxFailure
}
func (h *sftpFS) allowed(ctx context.Context, space int64, write bool) error {
	if e := h.authenticated(ctx, h.app.db); e != nil {
		return e
	}
	var admin bool
	if e := h.app.db.GetContext(ctx, &admin, "SELECT is_admin FROM users WHERE id=? AND deleted_at IS NULL", h.user); e != nil {
		return os.ErrPermission
	}
	if admin {
		return nil
	}
	var role string
	e := h.app.db.GetContext(ctx, &role, "SELECT role FROM members WHERE space_id=? AND user_id=?", space, h.user)
	if e != nil || write && role == "viewer" {
		return os.ErrPermission
	}
	return nil
}
func (h *sftpFS) live(ctx context.Context, space, id int64, write bool) error {
	if e := h.allowed(ctx, space, write); e != nil {
		return e
	}
	var n int
	e := h.app.db.GetContext(ctx, &n, "SELECT COUNT(*) FROM entries WHERE id=? AND space_id=? AND deleted_at IS NULL", id, space)
	if e != nil {
		return e
	}
	if n != 1 {
		return os.ErrNotExist
	}
	return nil
}

type virtualInfo struct {
	name                string
	size                int64
	modified            time.Time
	directory, writable bool
}

func (i virtualInfo) Name() string       { return i.name }
func (i virtualInfo) Size() int64        { return i.size }
func (i virtualInfo) ModTime() time.Time { return i.modified }
func (i virtualInfo) IsDir() bool        { return i.directory }
func (i virtualInfo) Sys() any           { return nil }
func (i virtualInfo) Mode() os.FileMode {
	var mode os.FileMode = 0444
	if i.writable {
		mode = 0664
	}
	if i.directory {
		mode |= os.ModeDir | 0111
	}
	return mode
}
func infoFor(n sftpNode) os.FileInfo {
	if n.entry == nil {
		return virtualInfo{name: n.name, directory: true, writable: !n.root && n.space.Role != "viewer", modified: time.Unix(0, 0)}
	}
	e := n.entry
	return virtualInfo{name: e.Name, size: e.Size, modified: e.UpdatedAt, directory: e.Folder, writable: n.space.Role != "viewer"}
}

type infoList []os.FileInfo

func (l infoList) ListAt(dst []os.FileInfo, offset int64) (int, error) {
	if offset < 0 {
		return 0, os.ErrInvalid
	}
	if offset >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(dst, l[offset:])
	if n < len(dst) {
		return n, io.EOF
	}
	return n, nil
}
func (h *sftpFS) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	if r.Method == "Stat" || r.Method == "Lstat" {
		if f := h.pending(r.Filepath); f != nil {
			return f.stat()
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	n, e := h.resolve(ctx, h.app.db, r.Filepath, false)
	if e != nil {
		return nil, sftpError(e)
	}
	if r.Method == "Stat" || r.Method == "Lstat" {
		if n.root {
			n.name = "/"
		}
		return infoList{infoFor(n)}, nil
	}
	if r.Method != "List" {
		return nil, sftp.ErrSSHFxOpUnsupported
	}
	list := infoList{}
	if n.root {
		spaces := []Space{}
		if e = h.app.db.SelectContext(ctx, &spaces, spaceStatSelect+spaceAccessFrom+" ORDER BY (s.personal AND s.owner_id=actor.id) DESC,s.name,s.id", h.user); e != nil {
			return nil, sftpError(e)
		}
		for _, s := range spaces {
			list = append(list, infoFor(sftpNode{space: s, name: h.spacePath(s)}))
		}
		return list, nil
	}
	if n.entry != nil && !n.entry.Folder {
		return nil, os.ErrInvalid
	}
	var parent *int64
	if n.entry != nil {
		id := n.entry.ID
		parent = &id
	}
	entries := []Entry{}
	if e = h.app.db.SelectContext(ctx, &entries, entrySelect+"WHERE e.space_id=? AND e.parent_id <=> ? AND e.deleted_at IS NULL ORDER BY e.folder DESC,e.name", n.space.ID, parent); e != nil {
		return nil, sftpError(e)
	}
	for i := range entries {
		list = append(list, infoFor(sftpNode{space: n.space, entry: &entries[i]}))
	}
	return list, nil
}

type sftpReadHandle struct {
	file      *os.File
	fs        *sftpFS
	space, id int64
}

func (f *sftpReadHandle) ReadAt(p []byte, off int64) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if e := f.fs.live(ctx, f.space, f.id, false); e != nil {
		return 0, sftpError(e)
	}
	return f.file.ReadAt(p, off)
}
func (f *sftpReadHandle) Close() error { return f.file.Close() }
func (h *sftpFS) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	n, e := h.resolve(ctx, h.app.db, r.Filepath, false)
	if e != nil {
		return nil, sftpError(e)
	}
	if n.entry == nil || n.entry.Folder {
		return nil, os.ErrInvalid
	}
	var key string
	if e = h.app.db.GetContext(ctx, &key, "SELECT blob_key FROM versions WHERE id=? AND entry_id=?", n.entry.CurrentVersion, n.entry.ID); e != nil {
		return nil, sftpError(e)
	}
	if !objectKey(key) {
		return nil, sftp.ErrSSHFxFailure
	}
	f, e := os.Open(filepath.Join(h.app.cfg.DataDir, key))
	if e != nil {
		return nil, sftpError(e)
	}
	return &sftpReadHandle{file: f, fs: h, space: n.space.ID, id: n.entry.ID}, nil
}
func objectKey(key string) bool {
	if len(key) != 64 {
		return false
	}
	for _, c := range key {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

func (h *sftpFS) Filecmd(r *sftp.Request) error {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	return sftpError(h.command(ctx, r))
}
func (h *sftpFS) command(ctx context.Context, r *sftp.Request) error {
	if r.Method != "Mkdir" && r.Method != "Remove" && r.Method != "Rmdir" && r.Method != "Rename" {
		return sftp.ErrSSHFxOpUnsupported
	}
	initial, e := h.resolve(ctx, h.app.db, r.Filepath, r.Method == "Mkdir")
	if e != nil {
		return e
	}
	if initial.root || initial.space.ID == 0 {
		return os.ErrPermission
	}
	tx, e := h.app.db.BeginTxx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = lockSpace(ctx, tx, initial.space.ID, h.user, true); e != nil {
		return os.ErrPermission
	}
	n, e := h.resolve(ctx, tx, r.Filepath, r.Method == "Mkdir")
	if e != nil {
		return e
	}
	if n.space.ID != initial.space.ID {
		return os.ErrPermission
	}
	switch r.Method {
	case "Mkdir":
		if n.entry != nil || n.virtual {
			return os.ErrExist
		}
		if !parentOK(ctx, tx, n.space.ID, n.parent) || !uniqueName(ctx, tx, n.space.ID, n.parent, n.name, 0) {
			return os.ErrExist
		}
		_, e = tx.ExecContext(ctx, "INSERT INTO entries(space_id,parent_id,name,folder,created_by,edited_by) VALUES(?,?,?,TRUE,?,?)", n.space.ID, n.parent, n.name, h.user, h.user)
	case "Remove", "Rmdir":
		if n.entry == nil {
			return os.ErrPermission
		}
		if n.entry.Folder != (r.Method == "Rmdir") {
			return os.ErrInvalid
		}
		if n.entry.Folder {
			var children int
			if e = tx.GetContext(ctx, &children, "SELECT COUNT(*) FROM entries WHERE parent_id=? AND deleted_at IS NULL", n.entry.ID); e != nil {
				return e
			}
			if children > 0 {
				return errors.New("directory not empty")
			}
		}
		_, e = tx.ExecContext(ctx, "UPDATE entries SET deleted_at=UTC_TIMESTAMP(6),edited_by=?,updated_at=UTC_TIMESTAMP(6) WHERE id=?", h.user, n.entry.ID)
	case "Rename":
		if n.entry == nil {
			return os.ErrPermission
		}
		dest, err := h.resolve(ctx, tx, r.Target, true)
		if err != nil {
			return err
		}
		if dest.root || dest.space.ID != n.space.ID || dest.virtual {
			return os.ErrPermission
		}
		if dest.entry != nil {
			if dest.entry.ID == n.entry.ID {
				return nil
			}
			return os.ErrExist
		}
		if !parentOK(ctx, tx, n.space.ID, dest.parent) || !uniqueName(ctx, tx, n.space.ID, dest.parent, dest.name, n.entry.ID) {
			return os.ErrExist
		}
		p := dest.parent
		for depth := 0; p != nil; depth++ {
			if *p == n.entry.ID || depth > 256 {
				return os.ErrInvalid
			}
			var row struct {
				Parent *int64 `db:"parent_id"`
			}
			if e = tx.GetContext(ctx, &row, "SELECT parent_id FROM entries WHERE id=? AND space_id=?", *p, n.space.ID); e != nil {
				return e
			}
			p = row.Parent
		}
		_, e = tx.ExecContext(ctx, "UPDATE entries SET parent_id=?,name=?,edited_by=?,updated_at=UTC_TIMESTAMP(6) WHERE id=?", dest.parent, dest.name, h.user, n.entry.ID)
	}
	if e != nil {
		return e
	}
	return tx.Commit()
}

// Interface assertions keep the virtual filesystem independent of host paths.
var _ sftp.FileReader = (*sftpFS)(nil)
var _ sftp.FileLister = (*sftpFS)(nil)
var _ sftp.FileCmder = (*sftpFS)(nil)
var _ sftpQuery = (*sqlx.Tx)(nil)
