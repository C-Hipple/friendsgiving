package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// Everyone shares one password. Knowing it earns a cookie holding an HMAC
// keyed by the password, so changing SITE_PASSWORD signs everyone out.
// Names are self-declared and trusted: they live in a plain cookie.
const (
	authCookie  = "bg_auth"
	nameCookie  = "bg_name"
	flashCookie = "bg_flash"
	cookieAge   = 400 * 24 * time.Hour // the longest browsers allow

	maxNameLen = 40
)

func authToken(password string) string {
	mac := hmac.New(sha256.New, []byte(password))
	mac.Write([]byte("brogiving-auth-v1"))
	return hex.EncodeToString(mac.Sum(nil))
}

func (a *app) authed(r *http.Request) bool {
	c, err := r.Cookie(authCookie)
	return err == nil && hmac.Equal([]byte(c.Value), []byte(a.authToken))
}

func (a *app) passwordOK(given string) bool {
	g, want := sha256.Sum256([]byte(given)), sha256.Sum256([]byte(a.cfg.Password))
	return subtle.ConstantTimeCompare(g[:], want[:]) == 1
}

func userName(r *http.Request) string {
	c, err := r.Cookie(nameCookie)
	if err != nil {
		return ""
	}
	name, err := url.QueryUnescape(c.Value)
	if err != nil {
		return ""
	}
	return cleanName(name)
}

func cleanName(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > maxNameLen {
		s = string([]rune(s)[:maxNameLen])
	}
	return s
}

func setCookie(w http.ResponseWriter, r *http.Request, name, value string, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   int(maxAge.Seconds()),
		HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteLaxMode,
	})
}

func setFlash(w http.ResponseWriter, r *http.Request, msg string) {
	setCookie(w, r, flashCookie, url.QueryEscape(msg), time.Minute)
}

// takeFlash returns the pending one-time message, if any, and clears it.
func takeFlash(w http.ResponseWriter, r *http.Request) string {
	c, err := r.Cookie(flashCookie)
	if err != nil {
		return ""
	}
	setCookie(w, r, flashCookie, "", -time.Second)
	msg, _ := url.QueryUnescape(c.Value)
	return msg
}

// localPath keeps post-login redirects on this site.
func localPath(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") {
		return "/"
	}
	return p
}

func (a *app) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/login" || p == "/healthz" || p == "/favicon.ico" || strings.HasPrefix(p, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		if !a.authed(r) {
			http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		if p != "/name" && userName(r) == "" {
			http.Redirect(w, r, "/name?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type gateForm struct {
	Next     string
	Name     string
	NeedName bool
	Error    string
}

func (a *app) loginPage(w http.ResponseWriter, r *http.Request) {
	next := localPath(r.URL.Query().Get("next"))
	if a.authed(r) {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	a.render(w, r, http.StatusOK, "login", view{Title: "Come on in", Bare: true,
		Page: gateForm{Next: next, NeedName: userName(r) == ""}})
}

func (a *app) login(w http.ResponseWriter, r *http.Request) {
	next := localPath(r.FormValue("next"))
	name := cleanName(r.FormValue("name"))
	needName := userName(r) == ""

	if !a.passwordOK(r.FormValue("password")) {
		time.Sleep(500 * time.Millisecond) // blunt brute-force speed bump
		a.render(w, r, http.StatusUnauthorized, "login", view{Title: "Come on in", Bare: true,
			Page: gateForm{Next: next, Name: name, NeedName: needName, Error: "That's not the password. Ask the host!"}})
		return
	}
	setCookie(w, r, authCookie, a.authToken, cookieAge)
	if name != "" {
		setCookie(w, r, nameCookie, url.QueryEscape(name), cookieAge)
		setFlash(w, r, "Welcome to the table, "+name+"!")
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (a *app) namePage(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, http.StatusOK, "name", view{Title: "Your name", Bare: true,
		Page: gateForm{Next: localPath(r.URL.Query().Get("next")), Name: userName(r)}})
}

func (a *app) setName(w http.ResponseWriter, r *http.Request) {
	next := localPath(r.FormValue("next"))
	name := cleanName(r.FormValue("name"))
	if name == "" {
		a.render(w, r, http.StatusUnprocessableEntity, "name", view{Title: "Your name", Bare: true,
			Page: gateForm{Next: next, Error: "We need something to put on your name tag."}})
		return
	}
	had := userName(r)
	setCookie(w, r, nameCookie, url.QueryEscape(name), cookieAge)
	if had == "" {
		setFlash(w, r, "Welcome to the table, "+name+"!")
	} else if had != name {
		setFlash(w, r, "You're now going by "+name+".")
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}
