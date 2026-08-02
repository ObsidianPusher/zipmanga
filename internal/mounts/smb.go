package mounts

import (
	"fmt"
	"io"
	"net"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"zipmanga/internal/db"

	"github.com/hirochachacha/go-smb2"
)

type SMBProvider struct {
	mount db.Mount
	root  string

	mu      sync.Mutex
	conn    net.Conn
	session *smb2.Session
	share   *smb2.Share
}

func NewSMBProvider(m db.Mount) (*SMBProvider, error) {
	if m.SMBHost == "" || m.SMBShare == "" {
		return nil, fmt.Errorf("smb mount requires smb_host and smb_share")
	}
	if m.SMBPort == 0 {
		m.SMBPort = 445
	}
	p := &SMBProvider{
		mount: m,
		root:  trimPath(m.RootPath),
	}
	if err := p.ensureShare(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *SMBProvider) ID() int64 { return p.mount.ID }

func (p *SMBProvider) Name() string { return p.mount.Name }

func (p *SMBProvider) Type() db.MountType { return p.mount.Type }

func (p *SMBProvider) ListDir(rel string) ([]FileEntry, error) {
	if err := p.ensureShare(); err != nil {
		return nil, err
	}

	sp := p.sharePath(rel)
	infos, err := p.share.ReadDir(sp)
	if err != nil {
		return nil, err
	}

	entries := make([]FileEntry, 0, len(infos))
	for _, info := range infos {
		base := trimPath(rel)
		filePath := trimPath(path.Join(base, info.Name()))
		entries = append(entries, FileEntry{
			Path:    filePath,
			Name:    info.Name(),
			IsDir:   info.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}
	filtered := make([]FileEntry, 0, len(entries))
	for _, entry := range entries {
		if ShouldHideEntry(entry) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered, nil
}

func (p *SMBProvider) Open(rel string) (io.ReadCloser, error) {
	if err := p.ensureShare(); err != nil {
		return nil, err
	}
	return p.share.Open(p.sharePath(rel))
}

func (p *SMBProvider) OpenReaderAt(rel string) (ReadAtCloser, int64, error) {
	if err := p.ensureShare(); err != nil {
		return nil, 0, err
	}
	f, err := p.share.Open(p.sharePath(rel))
	if err != nil {
		return nil, 0, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	return f, st.Size(), nil
}

func (p *SMBProvider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.share != nil {
		_ = p.share.Umount()
		p.share = nil
	}
	if p.session != nil {
		_ = p.session.Logoff()
		p.session = nil
	}
	if p.conn != nil {
		_ = p.conn.Close()
		p.conn = nil
	}
	return nil
}

func (p *SMBProvider) ensureShare() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.share != nil {
		return nil
	}

	addr := buildSMBAddress(p.mount.SMBHost, p.mount.SMBPort)
	netDialer := &net.Dialer{
		Timeout:   0,
		KeepAlive: 30 * time.Second,
	}
	conn, err := netDialer.Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("dial smb %s: %w", addr, err)
	}

	smbDialer := &smb2.Dialer{
		Initiator: &smb2.NTLMInitiator{
			User:     p.mount.SMBUser,
			Password: p.mount.SMBPassword,
			Domain:   p.mount.SMBDomain,
		},
	}
	ss, err := smbDialer.Dial(conn)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smb auth: %w", err)
	}

	share, err := ss.Mount(p.mount.SMBShare)
	if err != nil {
		_ = ss.Logoff()
		_ = conn.Close()
		return fmt.Errorf("smb mount share: %w", err)
	}

	p.conn = conn
	p.session = ss
	p.share = share
	return nil
}

func (p *SMBProvider) sharePath(rel string) string {
	r := trimPath(rel)
	if p.root == "" {
		return r
	}
	if r == "" {
		return p.root
	}
	return trimPath(path.Join(p.root, r))
}

func trimPath(v string) string {
	v = strings.TrimSpace(v)
	v = strings.ReplaceAll(v, "\\", "/")
	v = path.Clean("/" + v)
	if v == "/" {
		return ""
	}
	return strings.TrimPrefix(v, "/")
}

func buildSMBAddress(host string, port int) string {
	return net.JoinHostPort(strings.TrimSpace(host), strconv.Itoa(port))
}
