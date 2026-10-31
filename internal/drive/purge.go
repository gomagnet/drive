package drive

import (
	"errors"
	"github.com/gin-gonic/gin"
	"log"
	"os"
	"path/filepath"
	"strings"
)

var errTreeDepth = errors.New("folder nesting exceeds 256 levels")
var errActiveFile = errors.New("file is not in trash")

// Purge commits metadata removal before unlinking immutable objects. A crash can
// leave orphan objects, but can never leave a live version pointing at removed data.
func (a *App) purge(c *gin.Context) {
	var in struct {
		Space int64   `json:"space"`
		IDs   []int64 `json:"ids"`
	}
	if c.ShouldBindJSON(&in) != nil || len(in.IDs) < 1 || len(in.IDs) > 200 {
		fail(c, 400, "Select up to 200 items")
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
	visited := map[int64]bool{}
	order := []int64{}
	keys := []string{}
	var walk func(int64, int) error
	walk = func(id int64, depth int) error {
		if visited[id] {
			return nil
		}
		if depth > 256 {
			return errTreeDepth
		}
		visited[id] = true
		var deleted bool
		if e := tx.GetContext(c, &deleted, "SELECT deleted_at IS NOT NULL FROM entries WHERE id=? AND space_id=?", id, in.Space); e != nil {
			return e
		}
		if !deleted {
			return errActiveFile
		}
		var children []int64
		if e := tx.SelectContext(c, &children, "SELECT id FROM entries WHERE parent_id=? AND space_id=?", id, in.Space); e != nil {
			return e
		}
		for _, child := range children {
			if e := walk(child, depth+1); e != nil {
				return e
			}
		}
		var objects []string
		if e := tx.SelectContext(c, &objects, "SELECT blob_key FROM versions WHERE entry_id=?", id); e != nil {
			return e
		}
		keys = append(keys, objects...)
		order = append(order, id)
		return nil
	}
	for _, id := range in.IDs {
		if e = walk(id, 0); e != nil {
			fail(c, 409, "Only trashed items can be permanently deleted; restore very deeply nested folders individually.")
			return
		}
	}
	for _, id := range order {
		for _, q := range []string{"DELETE FROM shares WHERE entry_id=?", "DELETE FROM versions WHERE entry_id=?", "DELETE FROM entries WHERE id=?"} {
			if _, e = tx.ExecContext(c, q, id); e != nil {
				problem(c, e)
				return
			}
		}
	}
	if e = tx.Commit(); e != nil {
		problem(c, e)
		return
	}
	for _, key := range keys {
		if len(key) == 64 && !strings.ContainsAny(key, "/\\.") {
			if e = os.Remove(filepath.Join(a.cfg.DataDir, key)); e != nil && !os.IsNotExist(e) {
				log.Printf("orphan object %s: %v", key, e)
			}
		}
	}
	c.Status(204)
}
