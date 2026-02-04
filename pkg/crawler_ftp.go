package raria2

import (
	"context"
	"crypto/tls"
	"net"
	"net/url"
	"path"
	"strings"

	"github.com/jlaffaye/ftp"
	"github.com/sirupsen/logrus"
)

type ftpListingEntry struct {
	name  string
	isDir bool
}

func (r *RAria2) getLinksByFTPWithContext(ctx context.Context, parsedURL *url.URL) ([]string, error) {
	entries, err := r.ftpListEntries(ctx, parsedURL)
	if err != nil {
		return nil, err
	}

	dirPath := parsedURL.Path
	if dirPath == "" {
		dirPath = "/"
	}
	if !strings.HasSuffix(dirPath, "/") {
		dirPath += "/"
	}

	links := make([]string, 0, len(entries))
	for _, e := range entries {
		child := *parsedURL
		child.Fragment = ""
		child.RawQuery = ""
		child.RawPath = ""
		child.Path = path.Join(dirPath, e.name)
		if e.isDir && !strings.HasSuffix(child.Path, "/") {
			child.Path += "/"
		}
		links = append(links, child.String())
	}

	return links, nil
}

func (r *RAria2) ftpListEntries(ctx context.Context, u *url.URL) ([]ftpListingEntry, error) {
	if r.ftpList != nil {
		return r.ftpList(ctx, u)
	}

	host := u.Hostname()
	port := u.Port()
	if port == "" {
		if strings.ToLower(u.Scheme) == "ftps" {
			port = "990"
		} else {
			port = "21"
		}
	}
	addr := net.JoinHostPort(host, port)

	options := []ftp.DialOption{ftp.DialWithContext(ctx), ftp.DialWithTimeout(r.HTTPTimeout)}
	if strings.ToLower(u.Scheme) == "ftps" {
		options = append(options, ftp.DialWithTLS(&tls.Config{ServerName: host}))
	}

	conn, err := ftp.Dial(addr, options...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if quitErr := conn.Quit(); quitErr != nil {
			logrus.WithError(quitErr).Debug("failed to close FTP connection")
		}
	}()

	user := "anonymous"
	pass := "anonymous"
	if u.User != nil {
		user = u.User.Username()
		if p, ok := u.User.Password(); ok {
			pass = p
		} else {
			pass = ""
		}
	}

	if err := conn.Login(user, pass); err != nil {
		return nil, err
	}

	listPath := u.Path
	if listPath == "" {
		listPath = "/"
	}

	entries, err := conn.List(listPath)
	if err != nil && !strings.HasSuffix(listPath, "/") {
		entries, err = conn.List(listPath + "/")
	}
	if err != nil {
		return nil, errNotHTML
	}

	if !strings.HasSuffix(u.Path, "/") {
		base := path.Base(u.Path)
		if len(entries) == 1 && entries[0] != nil && entries[0].Name == base && entries[0].Type == ftp.EntryTypeFile {
			return nil, errNotHTML
		}
	}

	out := make([]ftpListingEntry, 0, len(entries))
	for _, e := range entries {
		if e == nil {
			continue
		}
		if e.Name == "" || e.Name == "." || e.Name == ".." {
			continue
		}
		if e.Type == ftp.EntryTypeLink {
			continue
		}
		out = append(out, ftpListingEntry{name: e.Name, isDir: e.Type == ftp.EntryTypeFolder})
	}

	return out, nil
}
