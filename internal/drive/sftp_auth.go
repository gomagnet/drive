package drive

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/ssh"
)

type sftpCredential struct {
	ID      int64     `db:"id" json:"id"`
	Name    string    `db:"name" json:"name"`
	Created time.Time `db:"created_at" json:"created_at"`
}

func (a *App) sftpCredentials(c *gin.Context) {
	list := []sftpCredential{}
	if e := a.db.SelectContext(c, &list, "SELECT id,name,created_at FROM sftp_credentials WHERE user_id=? ORDER BY id DESC", uid(c)); e != nil {
		problem(c, e)
		return
	}
	u, _ := url.Parse(a.cfg.PublicURL)
	_, port, _ := net.SplitHostPort(a.cfg.SFTP.Listen)
	c.JSON(200, gin.H{"enabled": a.cfg.SFTP.Listen != "", "host": u.Hostname(), "port": port, "host_key": a.sftpFingerprint, "username": c.MustGet("user").(User).Email, "credentials": list})
}
func (a *App) createSFTPCredential(c *gin.Context) {
	if a.cfg.SFTP.Listen == "" {
		fail(c, 409, "SFTP is not configured on this server")
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if c.ShouldBindJSON(&in) != nil || !validName(in.Name) || len(in.Name) > 160 {
		fail(c, 400, "Enter a credential name up to 160 bytes")
		return
	}
	tx, e := a.db.BeginTxx(c, nil)
	if e != nil {
		problem(c, e)
		return
	}
	defer tx.Rollback()
	var id int64
	if e = tx.GetContext(c, &id, "SELECT id FROM users WHERE id=? FOR UPDATE", uid(c)); e != nil {
		problem(c, e)
		return
	}
	var count int
	if e = tx.GetContext(c, &count, "SELECT COUNT(*) FROM sftp_credentials WHERE user_id=?", id); e != nil {
		problem(c, e)
		return
	}
	if count >= 20 {
		fail(c, 409, "Revoke an existing SFTP password before creating another")
		return
	}
	token := "md_sftp_" + randomToken()
	if _, e = tx.ExecContext(c, "INSERT INTO sftp_credentials(user_id,token_hash,name) VALUES(?,?,?)", id, digest(token), in.Name); e == nil {
		e = tx.Commit()
	}
	if e != nil {
		problem(c, e)
		return
	}
	c.JSON(201, gin.H{"password": token})
}
func (a *App) deleteSFTPCredential(c *gin.Context) {
	if _, e := a.db.ExecContext(c, "DELETE FROM sftp_credentials WHERE id=? AND user_id=?", number(c.Param("id")), uid(c)); e != nil {
		problem(c, e)
		return
	}
	c.Status(204)
}

func (a *App) sftpPassword(meta ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
	denied := errors.New("invalid SFTP credentials")
	host, _, _ := net.SplitHostPort(meta.RemoteAddr().String())
	if !a.loginRate.allow(host) {
		return nil, denied
	}
	select {
	case a.authSlots <- struct{}{}:
		defer func() { <-a.authSlots }()
	default:
		return nil, denied
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var row struct {
		ID   int64
		Hash string `db:"password_hash"`
	}
	if e := a.db.GetContext(ctx, &row, "SELECT id,password_hash FROM users WHERE email=? AND deleted_at IS NULL", email(meta.User())); e != nil {
		return nil, denied
	}
	ext := map[string]string{"user": strconv.FormatInt(row.ID, 10)}
	if strings.HasPrefix(string(password), "md_sftp_") {
		var id int64
		if e := a.db.GetContext(ctx, &id, "SELECT id FROM sftp_credentials WHERE user_id=? AND token_hash=?", row.ID, digest(string(password))); e != nil {
			return nil, denied
		}
		ext["credential"] = strconv.FormatInt(id, 10)
	} else {
		if row.Hash == "" || len(password) > 72 || bcrypt.CompareHashAndPassword([]byte(row.Hash), password) != nil {
			return nil, denied
		}
		ext["password_hash"] = digest(row.Hash)
	}
	return &ssh.Permissions{Extensions: ext}, nil
}
func (a *App) sftpHostKey() (ssh.Signer, error) {
	name := a.cfg.SFTP.HostKey
	if name == "" {
		name = filepath.Join(a.cfg.DataDir, "sftp_host_ed25519")
	}
	data, e := os.ReadFile(name)
	if os.IsNotExist(e) {
		_, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		block, err := ssh.MarshalPrivateKey(private, "")
		if err != nil {
			return nil, err
		}
		data = pem.EncodeToMemory(block)
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if os.IsExist(err) {
			data, e = os.ReadFile(name)
		} else if err != nil {
			return nil, err
		} else {
			_, e = f.Write(data)
			if e == nil {
				e = f.Sync()
			}
			closeErr := f.Close()
			if e == nil {
				e = closeErr
			}
		}
	}
	if e != nil {
		return nil, e
	}
	return ssh.ParsePrivateKey(data)
}

type idleSSHConn struct {
	net.Conn
	active atomic.Bool
}

func (c *idleSSHConn) Read(p []byte) (int, error) {
	if c.active.Load() {
		_ = c.SetReadDeadline(time.Now().Add(15 * time.Minute))
	}
	return c.Conn.Read(p)
}
func (c *idleSSHConn) Write(p []byte) (int, error) {
	if c.active.Load() {
		_ = c.SetWriteDeadline(time.Now().Add(time.Minute))
	}
	return c.Conn.Write(p)
}
func (a *App) startSFTP() (func(), error) {
	if a.cfg.SFTP.Listen == "" {
		return func() {}, nil
	}
	key, e := a.sftpHostKey()
	if e != nil {
		return nil, fmt.Errorf("SFTP host key: %w", e)
	}
	listener, e := net.Listen("tcp", a.cfg.SFTP.Listen)
	if e != nil {
		return nil, fmt.Errorf("SFTP listen: %w", e)
	}
	config := &ssh.ServerConfig{PasswordCallback: a.sftpPassword, MaxAuthTries: 3, ServerVersion: "SSH-2.0-MagnetDrive"}
	config.AddHostKey(key)
	a.sftpFingerprint = ssh.FingerprintSHA256(key.PublicKey())
	var mu sync.Mutex
	connections := map[net.Conn]bool{}
	var wg sync.WaitGroup
	done := make(chan struct{})
	slots := make(chan struct{}, 64)
	go func() {
		defer close(done)
		for {
			conn, e := listener.Accept()
			if e != nil {
				if !errors.Is(e, net.ErrClosed) {
					log.Printf("SFTP listener: %v", e)
				}
				return
			}
			select {
			case slots <- struct{}{}:
			default:
				conn.Close()
				continue
			}
			mu.Lock()
			connections[conn] = true
			mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-slots; conn.Close(); mu.Lock(); delete(connections, conn); mu.Unlock() }()
				a.serveSSH(conn, config)
			}()
		}
	}()
	log.Printf("SFTP listening on %s · host key %s", listener.Addr(), ssh.FingerprintSHA256(key.PublicKey()))
	return func() {
		listener.Close()
		<-done
		mu.Lock()
		for conn := range connections {
			conn.Close()
		}
		mu.Unlock()
		wg.Wait()
	}, nil
}
func (a *App) serveSSH(conn net.Conn, config *ssh.ServerConfig) {
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	transport := &idleSSHConn{Conn: conn}
	sshConn, chans, reqs, e := ssh.NewServerConn(transport, config)
	if e != nil {
		return
	}
	defer sshConn.Close()
	_ = conn.SetDeadline(time.Time{})
	transport.active.Store(true)
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Minute))
	go ssh.DiscardRequests(reqs)
	// One session per connection; no shell, exec, forwarding, or additional channels.
	used := false
	var wg sync.WaitGroup
	defer wg.Wait()
	for newChannel := range chans {
		if used || newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.Prohibited, "SFTP subsystem only")
			continue
		}
		used = true
		channel, requests, e := newChannel.Accept()
		if e != nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer channel.Close()
			for req := range requests {
				var payload struct{ Name string }
				ok := req.Type == "subsystem" && ssh.Unmarshal(req.Payload, &payload) == nil && payload.Name == "sftp"
				_ = req.Reply(ok, nil)
				if !ok {
					continue
				}
				go ssh.DiscardRequests(requests)
				handler := &sftpFS{app: a, user: number(sshConn.Permissions.Extensions["user"]), credential: number(sshConn.Permissions.Extensions["credential"]), passwordHash: sshConn.Permissions.Extensions["password_hash"]}
				server := sftp.NewRequestServer(channel, sftp.Handlers{FileGet: handler, FilePut: handler, FileCmd: handler, FileList: handler})
				if e := server.Serve(); e != nil && !errors.Is(e, io.EOF) {
					log.Printf("SFTP session user %d ended: %v", handler.user, e)
				}
				_ = server.Close()
				return
			}
		}()
	}
}
