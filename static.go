package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"sort"
	"strings"
)

type asset struct {
	data  []byte
	gz    []byte // nil when compression doesn't help (fonts, images)
	ctype string
	etag  string
}

// staticFiles serves web/static from memory. Every file is read and, where it
// helps, gzipped once at startup. Templates link to assets with ?v=<version>,
// a hash of all static content, so those URLs can be cached forever.
type staticFiles struct {
	files   map[string]*asset
	version string
}

func loadStatic(fsys fs.FS) (*staticFiles, error) {
	// Go's built-in table lacks woff2, and the container has no /etc/mime.types.
	mime.AddExtensionType(".woff2", "font/woff2")

	s := &staticFiles{files: map[string]*asset{}}
	var names []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		names = append(names, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(names)

	all := sha256.New()
	contents := make([][]byte, len(names))
	for i, name := range names {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(data)
		all.Write(sum[:])
		contents[i] = data
	}
	s.version = hex.EncodeToString(all.Sum(nil)[:5])

	for i, name := range names {
		data := contents[i]
		if path.Ext(name) == ".css" {
			// Version the stylesheet's font URLs so they match the
			// <link rel=preload> URLs and share their cache entry.
			data = bytes.ReplaceAll(data, []byte(".woff2)"), []byte(".woff2?v="+s.version+")"))
		}
		sum := sha256.Sum256(data)
		a := &asset{
			data:  data,
			ctype: mime.TypeByExtension(path.Ext(name)),
			etag:  `"` + hex.EncodeToString(sum[:8]) + `"`,
		}
		if a.ctype == "" {
			a.ctype = "application/octet-stream"
		}
		if compressible(a.ctype) {
			var buf bytes.Buffer
			zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
			zw.Write(data)
			zw.Close()
			if buf.Len() < len(data) {
				a.gz = buf.Bytes()
			}
		}
		s.files["/static/"+name] = a
	}
	return s, nil
}

func compressible(ctype string) bool {
	return strings.HasPrefix(ctype, "text/") ||
		strings.Contains(ctype, "javascript") ||
		strings.Contains(ctype, "svg")
}

// url returns the cache-busting URL for a static file, e.g. "app.css".
func (s *staticFiles) url(name string) string {
	return "/static/" + name + "?v=" + s.version
}

func (s *staticFiles) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.serve(w, r, r.URL.Path)
}

func (s *staticFiles) favicon(w http.ResponseWriter, r *http.Request) {
	s.serve(w, r, "/static/favicon.svg")
}

func (s *staticFiles) serve(w http.ResponseWriter, r *http.Request, name string) {
	a, ok := s.files[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", a.ctype)
	h.Set("ETag", a.etag)
	h.Set("Vary", "Accept-Encoding")
	if r.URL.Query().Get("v") == s.version {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "no-cache")
	}
	if r.Header.Get("If-None-Match") == a.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	body := a.data
	if a.gz != nil && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		h.Set("Content-Encoding", "gzip")
		body = a.gz
	}
	w.Write(body)
}
