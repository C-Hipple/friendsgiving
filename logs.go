package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
)

// Names are self-declared, so every POST goes in the post log: where it went,
// the name it was sent under, the address it came from, and what it said. The
// log is readable at /logs, which nothing links to.

const logsShown = 500

// auditPosts records each POST once it's been handled, so the entry carries
// the response status.
func (a *app) auditPosts(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		r.ParseForm() // handlers reuse the parsed form
		form := make(url.Values, len(r.PostForm))
		for k, vs := range r.PostForm {
			if k == "password" {
				vs = []string{"[redacted]"}
			}
			form[k] = vs
		}
		entry := PostLog{Path: r.URL.Path, User: userName(r), IP: a.clientIP(r), Form: form}

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		entry.Status = rec.status
		if err := a.store.LogPost(context.WithoutCancel(r.Context()), entry); err != nil {
			log.Printf("error: post log: %v", err)
		}
	})
}

// clientIP is the address a request came from. Fly's proxy reports it in
// Fly-Client-IP. Other reverse proxies append it to X-Forwarded-For, which is
// only believed from a private or loopback peer, since anyone connecting
// directly could send their own.
func (a *app) clientIP(r *http.Request) string {
	if a.cfg.OnFly {
		if ip := r.Header.Get("Fly-Client-IP"); ip != "" {
			return normalizeIP(ip)
		}
	}
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	if addr, err := netip.ParseAddr(peer); err == nil && (addr.Unmap().IsLoopback() || addr.Unmap().IsPrivate()) {
		if fwd := r.Header.Values("X-Forwarded-For"); len(fwd) > 0 {
			hops := strings.Split(fwd[len(fwd)-1], ",")
			if hop := strings.TrimSpace(hops[len(hops)-1]); hop != "" {
				return normalizeIP(hop)
			}
		}
	}
	return normalizeIP(peer)
}

// normalizeIP writes IPv4 addresses as IPv4 even when they arrive in IPv6
// form (::ffff:1.2.3.4).
func normalizeIP(s string) string {
	if addr, err := netip.ParseAddr(s); err == nil {
		return addr.Unmap().String()
	}
	return s
}

type logsPage struct {
	Filter  LogFilter
	Entries []PostLog
	Total   int // entries matching Filter; Entries holds the newest of them
	Users   []string
	Paths   []string
	IPs     []string
}

func (a *app) logs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p := logsPage{Filter: LogFilter{
		User: strings.TrimSpace(q.Get("user")),
		Path: strings.TrimSpace(q.Get("path")),
		IP:   strings.TrimSpace(q.Get("ip")),
		Text: strings.TrimSpace(q.Get("q")),
	}}
	ctx := r.Context()
	var err error
	if p.Entries, p.Total, err = a.store.PostLogs(ctx, p.Filter, logsShown); err != nil {
		a.serverError(w, r, err)
		return
	}
	if p.Users, p.Paths, p.IPs, err = a.store.PostLogSuggestions(ctx); err != nil {
		a.serverError(w, r, err)
		return
	}
	a.render(w, r, http.StatusOK, "logs", view{Title: "Post log", Page: p})
}
