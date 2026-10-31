package drive

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"sync"
	"time"

	"github.com/pkg/sftp"
)

type sftpWriteHandle struct {
	mu                          sync.Mutex
	fs                          *sftpFS
	file                        *os.File
	key, path                   string
	original                    sftpNode
	append, read, dirty, closed bool
	failed                      error
}

func (h *sftpFS) Filewrite(r *sftp.Request) (io.WriterAt, error)          { return h.openWrite(r) }
func (h *sftpFS) OpenFile(r *sftp.Request) (sftp.WriterAtReaderAt, error) { return h.openWrite(r) }
func (h *sftpFS) openWrite(r *sftp.Request) (*sftpWriteHandle, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	flags := r.Pflags()
	if !flags.Write && !flags.Append {
		return nil, os.ErrPermission
	}
	n, e := h.resolve(ctx, h.app.db, r.Filepath, flags.Creat)
	if e != nil {
		return nil, sftpError(e)
	}
	if n.root || n.virtual || n.entry != nil && n.entry.Folder {
		return nil, os.ErrPermission
	}
	if e = h.allowed(ctx, n.space.ID, true); e != nil {
		return nil, sftpError(e)
	}
	if flags.Excl && n.entry != nil {
		return nil, os.ErrExist
	}
	select {
	case h.app.uploadSlots <- struct{}{}:
	default:
		return nil, sftp.ErrSSHFxFailure
	}
	ok := false
	key := randomToken()
	f, e := os.OpenFile(filepath.Join(h.app.cfg.DataDir, key), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		<-h.app.uploadSlots
		return nil, sftpError(e)
	}
	defer func() {
		if !ok {
			f.Close()
			os.Remove(f.Name())
			<-h.app.uploadSlots
		}
	}()
	if n.entry != nil && !flags.Trunc {
		if n.entry.Size > h.app.cfg.MaxUpload {
			return nil, sftp.ErrSSHFxFailure
		}
		var old string
		if e = h.app.db.GetContext(ctx, &old, "SELECT blob_key FROM versions WHERE id=? AND entry_id=?", n.entry.CurrentVersion, n.entry.ID); e != nil {
			return nil, sftpError(e)
		}
		if !objectKey(old) {
			return nil, sftp.ErrSSHFxFailure
		}
		source, err := os.Open(filepath.Join(h.app.cfg.DataDir, old))
		if err != nil {
			return nil, sftpError(err)
		}
		_, err = io.Copy(f, io.LimitReader(source, h.app.cfg.MaxUpload+1))
		source.Close()
		if err != nil {
			return nil, sftpError(err)
		}
	}
	ok = true
	handle := &sftpWriteHandle{fs: h, file: f, key: key, path: r.Filepath, original: n, append: flags.Append, read: flags.Read, dirty: n.entry == nil || flags.Trunc}
	h.writesMu.Lock()
	if h.writes == nil {
		h.writes = make(map[string]*sftpWriteHandle)
	}
	if h.writes[path.Clean(r.Filepath)] != nil {
		h.writesMu.Unlock()
		ok = false
		return nil, sftp.ErrSSHFxFailure
	}
	h.writes[path.Clean(r.Filepath)] = handle
	h.writesMu.Unlock()
	return handle, nil
}
func (h *sftpFS) pending(name string) *sftpWriteHandle {
	h.writesMu.Lock()
	defer h.writesMu.Unlock()
	return h.writes[path.Clean(name)]
}
func (f *sftpWriteHandle) stat() (sftp.ListerAt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.check(); e != nil {
		return nil, e
	}
	stat, e := f.file.Stat()
	if e != nil {
		return nil, sftpError(e)
	}
	return infoList{virtualInfo{name: f.original.name, size: stat.Size(), modified: stat.ModTime(), writable: true}}, nil
}
func (f *sftpWriteHandle) check() error {
	if f.closed {
		return os.ErrClosed
	}
	if f.failed != nil {
		return f.failed
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var e error
	if f.original.entry == nil {
		e = f.fs.allowed(ctx, f.original.space.ID, true)
	} else {
		e = f.fs.live(ctx, f.original.space.ID, f.original.entry.ID, true)
	}
	if e != nil {
		f.failed = sftpError(e)
	}
	return f.failed
}
func (f *sftpWriteHandle) WriteAt(p []byte, off int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.check(); e != nil {
		return 0, e
	}
	if f.append {
		stat, e := f.file.Stat()
		if e != nil {
			f.failed = sftpError(e)
			return 0, f.failed
		}
		off = stat.Size()
	}
	if off < 0 || off > f.fs.app.cfg.MaxUpload || int64(len(p)) > f.fs.app.cfg.MaxUpload-off {
		f.failed = sftp.ErrSSHFxFailure
		return 0, f.failed
	}
	n, e := f.file.WriteAt(p, off)
	f.dirty = true
	if e != nil {
		f.failed = sftpError(e)
		return n, f.failed
	}
	return n, e
}
func (f *sftpWriteHandle) ReadAt(p []byte, off int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.read {
		return 0, os.ErrPermission
	}
	if e := f.check(); e != nil {
		return 0, e
	}
	return f.file.ReadAt(p, off)
}
func (f *sftpWriteHandle) TransferError(e error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e != nil {
		f.failed = sftp.ErrSSHFxFailure
	}
}
func sameVersion(a, b *int64) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
func (f *sftpWriteHandle) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	committed := false
	defer func() {
		f.closed = true
		f.file.Close()
		if !committed {
			_ = os.Remove(f.file.Name())
		}
		<-f.fs.app.uploadSlots
		f.fs.writesMu.Lock()
		delete(f.fs.writes, path.Clean(f.path))
		f.fs.writesMu.Unlock()
	}()
	if e := f.check(); e != nil {
		return e
	}
	if !f.dirty {
		return nil
	}
	stat, e := f.file.Stat()
	if e != nil {
		return sftpError(e)
	}
	size := stat.Size()
	if size > f.fs.app.cfg.MaxUpload {
		return sftp.ErrSSHFxFailure
	}
	head := make([]byte, 512)
	count, e := f.file.ReadAt(head, 0)
	if e != nil && !errors.Is(e, io.EOF) {
		return sftpError(e)
	}
	kind := contentType(f.original.name, head[:count])
	if e = f.file.Sync(); e != nil {
		return sftpError(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tx, e := f.fs.app.db.BeginTxx(ctx, nil)
	if e != nil {
		return sftpError(e)
	}
	defer tx.Rollback()
	if e = lockSpace(ctx, tx, f.original.space.ID, f.fs.user, true); e != nil {
		return os.ErrPermission
	}
	n, e := f.fs.resolve(ctx, tx, f.path, true)
	if e != nil {
		return sftpError(e)
	}
	if n.root || n.virtual || n.space.ID != f.original.space.ID {
		return os.ErrPermission
	}
	if f.original.entry == nil {
		if n.entry != nil {
			return os.ErrExist
		}
	} else if n.entry == nil || n.entry.ID != f.original.entry.ID || !sameVersion(n.entry.CurrentVersion, f.original.entry.CurrentVersion) {
		return sftp.ErrSSHFxFailure
	}
	var used int64
	if e = tx.GetContext(ctx, &used, "SELECT COALESCE(SUM(v.size),0) FROM versions v JOIN entries e ON e.id=v.entry_id WHERE e.space_id=?", n.space.ID); e != nil {
		return sftpError(e)
	}
	if size > n.space.Quota-used {
		return sftp.ErrSSHFxFailure
	}
	var id int64
	if n.entry != nil {
		id = n.entry.ID
	} else {
		if !parentOK(ctx, tx, n.space.ID, n.parent) || !uniqueName(ctx, tx, n.space.ID, n.parent, n.name, 0) {
			return os.ErrExist
		}
		res, err := tx.ExecContext(ctx, "INSERT INTO entries(space_id,parent_id,name,created_by,edited_by) VALUES(?,?,?,?,?)", n.space.ID, n.parent, n.name, f.fs.user, f.fs.user)
		if err != nil {
			return sftpError(err)
		}
		id, e = res.LastInsertId()
		if e != nil {
			return sftpError(e)
		}
	}
	res, e := tx.ExecContext(ctx, "INSERT INTO versions(entry_id,blob_key,size,mime,created_by) VALUES(?,?,?,?,?)", id, f.key, size, kind, f.fs.user)
	if e != nil {
		return sftpError(e)
	}
	version, e := res.LastInsertId()
	if e != nil {
		return sftpError(e)
	}
	if _, e = tx.ExecContext(ctx, "UPDATE entries SET current_version=?,edited_by=?,updated_at=UTC_TIMESTAMP(6) WHERE id=?", version, f.fs.user, id); e != nil {
		return sftpError(e)
	}
	if e = tx.Commit(); e != nil {
		return sftpError(e)
	}
	committed = true
	return nil
}

var _ sftp.OpenFileWriter = (*sftpFS)(nil)
var _ sftp.TransferError = (*sftpWriteHandle)(nil)
