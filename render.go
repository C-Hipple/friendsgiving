package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"hash/fnv"
	"html"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

type app struct {
	cfg       config
	store     *Store
	static    *staticFiles
	pages     map[string]*template.Template
	authToken string
}

func newApp(cfg config, store *Store) (*app, error) {
	staticFS, err := fs.Sub(webFS, "web/static")
	if err != nil {
		return nil, err
	}
	st, err := loadStatic(staticFS)
	if err != nil {
		return nil, fmt.Errorf("load static files: %w", err)
	}
	a := &app{cfg: cfg, store: store, static: st, authToken: authToken(cfg.Password)}
	if err := a.parseTemplates(); err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	return a, nil
}

func (a *app) parseTemplates() error {
	funcs := template.FuncMap{
		"asset":    a.static.url,
		"icon":     a.icon,
		"initials": initials,
		"tone":     tone,
		"ago":      ago,
		"stamp":    stamp,
		"same":     sameName,
		"category": categoryByKey,
		"tag":      tagByKey,
		"lower":    strings.ToLower,
		"courses":  courseList,
		"plural":   plural,
		"excerpt":  excerpt,
		"pct":      pct,
		"dict":     dict,
		"tbd":      func(s string) bool { return strings.EqualFold(s, "TBD") },
		"mapsURL": func(addr string) string {
			return "https://www.google.com/maps/search/?api=1&query=" + url.QueryEscape(addr)
		},
		"categories": func() []category { return categories },
		"dietTags":   func() []dietTag { return dietTags },
	}
	pages, err := fs.Glob(webFS, "web/templates/pages/*.html")
	if err != nil {
		return err
	}
	a.pages = map[string]*template.Template{}
	for _, p := range pages {
		t, err := template.New(path.Base(p)).Funcs(funcs).ParseFS(webFS,
			"web/templates/layout.html", "web/templates/partials.html", p)
		if err != nil {
			return err
		}
		a.pages[strings.TrimSuffix(path.Base(p), ".html")] = t
	}
	return nil
}

// view is the data every page template receives; Page carries the
// page-specific part.
type view struct {
	Title  string
	Nav    string
	Bare   bool // login and name pages: no navigation chrome
	User   string
	Path   string // current URL, to come back to after changing names
	Event  eventInfo
	Counts Counts
	Flash  string
	Page   any
}

var (
	bufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}
	gzPool  = sync.Pool{New: func() any { w, _ := gzip.NewWriterLevel(nil, gzip.DefaultCompression); return w }}
)

func (a *app) render(w http.ResponseWriter, r *http.Request, status int, page string, v view) {
	v.Event = a.cfg.Event
	v.User = userName(r)
	v.Path = r.URL.RequestURI()
	v.Flash = takeFlash(w, r)
	if !v.Bare {
		c, err := a.store.Counts(r.Context())
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		v.Counts = c
	}

	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)
	if err := a.pages[page].ExecuteTemplate(buf, "layout", v); err != nil {
		a.serverError(w, r, err)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Vary", "Accept-Encoding")
	if buf.Len() < 1024 || !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.WriteHeader(status)
		w.Write(buf.Bytes())
		return
	}
	h.Set("Content-Encoding", "gzip")
	w.WriteHeader(status)
	zw := gzPool.Get().(*gzip.Writer)
	zw.Reset(w)
	zw.Write(buf.Bytes())
	zw.Close()
	gzPool.Put(zw)
}

func (a *app) serverError(w http.ResponseWriter, r *http.Request, err error) {
	log.Printf("error: %s %s: %v", r.Method, r.URL.Path, err)
	http.Error(w, "Something burned in the kitchen. Try again in a moment.", http.StatusInternalServerError)
}

func (a *app) notFound(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, http.StatusNotFound, "notfound", view{Title: "Not found"})
}

func (a *app) icon(name string, class ...string) template.HTML {
	cls := "icon"
	if len(class) > 0 {
		cls += " " + strings.Join(class, " ")
	}
	return template.HTML(fmt.Sprintf(`<svg class="%s" aria-hidden="true"><use href="%s#%s"></use></svg>`,
		html.EscapeString(cls), a.static.url("icons.svg"), html.EscapeString(name)))
}

func initials(name string) string {
	var out []rune
	for _, word := range strings.Fields(name) {
		r, _ := utf8.DecodeRuneInString(word)
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			out = append(out, unicode.ToUpper(r))
		}
		if len(out) == 2 {
			break
		}
	}
	if len(out) == 0 {
		return "?"
	}
	return string(out)
}

// tone picks a stable avatar color for a name.
func tone(name string) string {
	h := fnv.New32a()
	h.Write([]byte(strings.ToLower(strings.TrimSpace(name))))
	return [...]string{"pumpkin", "cranberry", "sage", "amber", "rose"}[h.Sum32()%5]
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case t.Year() == time.Now().Year():
		return t.Format("Jan 2")
	default:
		return t.Format("Jan 2, 2006")
	}
}

func stamp(t time.Time) string { return t.Format("Mon Jan 2, 2006 at 3:04 PM") }

func sameName(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	return a != "" && strings.EqualFold(a, b)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func excerpt(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)[:n]
	if i := strings.LastIndexByte(string(r), ' '); i > n/2 {
		return string(r)[:i] + "…"
	}
	return string(r) + "…"
}

func pct(n, total int) int {
	if total <= 0 {
		return 0
	}
	return n * 100 / total
}

func dict(kv ...any) (map[string]any, error) {
	if len(kv)%2 != 0 {
		return nil, fmt.Errorf("dict: odd number of arguments")
	}
	m := make(map[string]any, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		k, ok := kv[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict: key %v is not a string", kv[i])
		}
		m[k] = kv[i+1]
	}
	return m, nil
}
