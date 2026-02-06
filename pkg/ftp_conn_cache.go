package raria2

import (
	"context"
	"crypto/tls"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jlaffaye/ftp"
	"github.com/sirupsen/logrus"
)

type ftpConnEntry struct {
	mu            sync.Mutex
	conn          *ftp.ServerConn
	lastUsed      time.Time
	dialAddr      string
	dialOpts      []ftp.DialOption
	user          string
	pass          string
	isImplicitTLS bool
}

func (e *ftpConnEntry) logFields() logrus.Fields {
	fields := logrus.Fields{
		"addr": e.dialAddr,
		"user": e.user,
	}
	if e.isImplicitTLS {
		fields["implicit_tls"] = true
	}
	return fields
}

func (r *RAria2) ftpConnKey(u *url.URL) (key string, addr string, user string, pass string, opts []ftp.DialOption, implicitTLS bool) {
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		if strings.ToLower(u.Scheme) == "ftps" {
			port = "990"
		} else {
			port = "21"
		}
	}
	addr = net.JoinHostPort(host, port)

	user = "anonymous"
	pass = "anonymous"
	if u.User != nil {
		user = u.User.Username()
		if p, ok := u.User.Password(); ok {
			pass = p
		} else {
			pass = ""
		}
	}

	implicitTLS = strings.ToLower(u.Scheme) == "ftps"

	opts = []ftp.DialOption{ftp.DialWithTimeout(r.HTTPTimeout)}
	if implicitTLS {
		opts = append(opts, ftp.DialWithTLS(&tls.Config{ServerName: host}))
	}

	// Include credentials in key: many servers apply different permissions per login.
	key = strings.ToLower(u.Scheme) + "://" + addr + "|" + user + ":" + pass
	return key, addr, user, pass, opts, implicitTLS
}

func (r *RAria2) ftpConnCacheGet(ctx context.Context, u *url.URL) (*ftpConnEntry, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	key, addr, user, pass, opts, implicitTLS := r.ftpConnKey(u)

	r.ftpConnMu.Lock()
	if r.ftpConnCache == nil {
		r.ftpConnCache = make(map[string]*ftpConnEntry)
	}
	entry := r.ftpConnCache[key]
	if entry == nil {
		entry = &ftpConnEntry{dialAddr: addr, dialOpts: opts, user: user, pass: pass, isImplicitTLS: implicitTLS}
		r.ftpConnCache[key] = entry
	}
	r.ftpConnMu.Unlock()

	// Ensure the connection is established (or re-established) under entry lock.
	entry.mu.Lock()
	defer entry.mu.Unlock()
	entry.lastUsed = time.Now()

	if entry.conn != nil {
		return entry, nil
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Dial supports context cancellation via option.
	dialOpts := append([]ftp.DialOption{ftp.DialWithContext(ctx)}, entry.dialOpts...)
	conn, err := ftp.Dial(entry.dialAddr, dialOpts...)
	if err != nil {
		return nil, err
	}
	if err := conn.Login(entry.user, entry.pass); err != nil {
		_ = conn.Quit()
		return nil, err
	}
	entry.conn = conn
	logrus.WithFields(entry.logFields()).Info("FTP connection established")
	return entry, nil
}

func (r *RAria2) ftpConnCacheCloseAll() {
	r.ftpConnMu.Lock()
	cache := r.ftpConnCache
	r.ftpConnCache = nil
	r.ftpConnMu.Unlock()

	for _, entry := range cache {
		if entry == nil {
			continue
		}
		entry.mu.Lock()
		entry.closeLocked()
		entry.mu.Unlock()
	}
}

func (e *ftpConnEntry) list(ctx context.Context, listPath string) ([]*ftp.Entry, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	entries, err := e.conn.List(listPath)
	if err != nil && !strings.HasSuffix(listPath, "/") {
		entries, err = e.conn.List(listPath + "/")
	}
	return entries, err
}

func (e *ftpConnEntry) closeLocked() {
	if e.conn == nil {
		return
	}
	logrus.WithFields(e.logFields()).Info("FTP connection closed")
	if err := e.conn.Quit(); err != nil {
		logrus.WithError(err).WithFields(e.logFields()).Debug("failed to close FTP connection")
	}
	e.conn = nil
}
