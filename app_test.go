package main

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) (*httptest.Server, *Store) {
	t.Helper()
	store, err := openStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	cfg := config{Password: "gobble", Event: eventInfo{Name: "Brogiving 2026", Date: "TBD", Time: "TBD", Location: "TBD"}}
	a, err := newApp(cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a.routes())
	t.Cleanup(srv.Close)
	return srv, store
}

// guest is a browser with its own cookie jar that doesn't follow redirects.
func guest(t *testing.T) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func loggedIn(t *testing.T, srv *httptest.Server, name string) *http.Client {
	t.Helper()
	c := guest(t)
	resp := post(t, c, srv.URL+"/login", url.Values{"password": {"gobble"}, "name": {name}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login as %s: status %d", name, resp.StatusCode)
	}
	return c
}

func post(t *testing.T, c *http.Client, u string, form url.Values) *http.Response {
	t.Helper()
	resp, err := c.PostForm(u, form)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func get(t *testing.T, c *http.Client, u string) (*http.Response, string) {
	t.Helper()
	resp, err := c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(b)
}

func TestPasswordGate(t *testing.T) {
	srv, _ := newTestServer(t)
	c := guest(t)

	resp, _ := get(t, c, srv.URL+"/potluck")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/login") {
		t.Fatalf("anonymous request: got %d to %q, want redirect to /login", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp := post(t, c, srv.URL+"/login", url.Values{"password": {"wrong"}, "name": {"Mallory"}}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password: got %d, want 401", resp.StatusCode)
	}
	resp = post(t, c, srv.URL+"/login", url.Values{"password": {"gobble"}, "next": {"//evil.example"}})
	if loc := resp.Header.Get("Location"); loc != "/" {
		t.Fatalf("open redirect: got Location %q, want /", loc)
	}
	// Right password but no name yet: must pick a name before anything else.
	resp, _ = get(t, c, srv.URL+"/potluck")
	if !strings.HasPrefix(resp.Header.Get("Location"), "/name") {
		t.Fatalf("nameless user: got Location %q, want /name", resp.Header.Get("Location"))
	}
	post(t, c, srv.URL+"/name", url.Values{"name": {"  Big   Mike "}})
	resp, body := get(t, c, srv.URL+"/potluck")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Big Mike") {
		t.Fatalf("named user: got %d, body mentions name: %v", resp.StatusCode, strings.Contains(body, "Big Mike"))
	}
	// Static assets stay reachable for the login page.
	if resp, _ := get(t, guest(t), srv.URL+"/static/app.css"); resp.StatusCode != http.StatusOK {
		t.Fatalf("static without auth: got %d", resp.StatusCode)
	}
}

func TestDishOwnership(t *testing.T) {
	srv, store := newTestServer(t)
	chris := loggedIn(t, srv, "Chris")
	mike := loggedIn(t, srv, "Mike")

	if resp := post(t, chris, srv.URL+"/potluck", url.Values{"name": {""}, "category": {"soup"}}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("invalid dish: got %d, want 422", resp.StatusCode)
	}
	post(t, chris, srv.URL+"/potluck", url.Values{
		"name": {"Turkey"}, "category": {"main"}, "needs_oven": {"1"}, "oven_notes": {"325°F, 4h"},
		"tags": {"gluten-free", "bogus"},
	})
	dishes, _ := store.ListDishes(context.Background())
	if len(dishes) != 1 {
		t.Fatalf("got %d dishes, want 1", len(dishes))
	}
	d := dishes[0]
	if d.BroughtBy != "Chris" || !d.NeedsOven || d.OvenNotes != "325°F, 4h" || strings.Join(d.Tags, ",") != "gluten-free" {
		t.Fatalf("stored dish = %+v", d)
	}

	// Mike can't edit or delete Chris's dish.
	post(t, mike, srv.URL+"/potluck/1", url.Values{"name": {"Tofurkey"}, "category": {"main"}})
	post(t, mike, srv.URL+"/potluck/1/delete", nil)
	if d, _ := store.GetDish(context.Background(), 1); d.Name != "Turkey" {
		t.Fatalf("another user changed the dish: %+v", d)
	}

	// Chris can; unchecking the oven clears its notes.
	post(t, chris, srv.URL+"/potluck/1", url.Values{"name": {"Smoked Turkey"}, "category": {"main"}, "oven_notes": {"stale"}})
	if d, _ := store.GetDish(context.Background(), 1); d.Name != "Smoked Turkey" || d.NeedsOven || d.OvenNotes != "" {
		t.Fatalf("after owner edit: %+v", d)
	}
	post(t, chris, srv.URL+"/potluck/1/delete", nil)
	if _, err := store.GetDish(context.Background(), 1); err != errNotFound {
		t.Fatalf("after delete: err = %v", err)
	}
}

func TestForumReplies(t *testing.T) {
	srv, store := newTestServer(t)
	chris := loggedIn(t, srv, "Chris")
	mike := loggedIn(t, srv, "Mike")
	ctx := context.Background()

	post(t, chris, srv.URL+"/forum", url.Values{"title": {"Carpool?"}, "body": {"<b>3 seats</b>"}})
	post(t, mike, srv.URL+"/forum/1/replies", url.Values{"body": {"Me!"}})
	post(t, chris, srv.URL+"/forum/1/replies", url.Values{"body": {"Done."}})
	if th, _ := store.GetThread(ctx, 1); th.ReplyCount != 2 {
		t.Fatalf("reply count = %d, want 2", th.ReplyCount)
	}

	_, body := get(t, chris, srv.URL+"/forum/1")
	if strings.Contains(body, "<b>3 seats</b>") || !strings.Contains(body, "&lt;b&gt;3 seats&lt;/b&gt;") {
		t.Fatal("post body was not HTML-escaped")
	}

	post(t, chris, srv.URL+"/forum/1/replies/1/delete", nil) // Mike's reply: refused
	post(t, mike, srv.URL+"/forum/1/replies/1/delete", nil)
	if th, _ := store.GetThread(ctx, 1); th.ReplyCount != 1 {
		t.Fatalf("reply count after delete = %d, want 1", th.ReplyCount)
	}

	post(t, mike, srv.URL+"/forum/1/delete", nil) // not Mike's thread
	if _, err := store.GetThread(ctx, 1); err != nil {
		t.Fatalf("thread deleted by non-author: %v", err)
	}
	post(t, chris, srv.URL+"/forum/1/delete", nil)
	if replies, _ := store.ListReplies(ctx, 1); len(replies) != 0 {
		t.Fatalf("replies survived thread delete: %d", len(replies))
	}
}

func TestPostLog(t *testing.T) {
	srv, store := newTestServer(t)
	ctx := context.Background()

	post(t, guest(t), srv.URL+"/login", url.Values{"password": {"turkey?"}, "name": {"Mallory"}})
	chris := loggedIn(t, srv, "Chris")
	post(t, chris, srv.URL+"/potluck", url.Values{"name": {"Pie"}, "category": {"dessert"}, "notes": {"Pecan,\nwith 100% butter"}})
	// A reverse proxy in front reports the real address.
	req, _ := http.NewRequest("POST", srv.URL+"/forum", strings.NewReader(url.Values{"title": {"Rides"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	resp, err := chris.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	all, total, err := store.PostLogs(ctx, LogFilter{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 || len(all) != 4 {
		t.Fatalf("got %d of %d entries, want 4", len(all), total)
	}
	if e := all[3]; e.Path != "/login" || e.Status != http.StatusUnauthorized || e.User != "" ||
		e.Form.Get("name") != "Mallory" || e.Form.Get("password") != "[redacted]" {
		t.Fatalf("failed login entry = %+v", e)
	}
	if e := all[1]; e.User != "Chris" || e.IP != "127.0.0.1" || e.Form.Get("notes") != "Pecan,\nwith 100% butter" {
		t.Fatalf("dish entry = %+v", e)
	}
	if e := all[0]; e.Path != "/forum" || e.IP != "203.0.113.9" || e.Status != http.StatusSeeOther {
		t.Fatalf("thread entry = %+v", e)
	}

	for _, c := range []struct {
		f    LogFilter
		want int
	}{
		{LogFilter{User: "chr"}, 2},
		{LogFilter{Path: "/login"}, 2},
		{LogFilter{IP: "203.0.113"}, 1},
		{LogFilter{User: "Chris", Path: "/potluck"}, 1},
		{LogFilter{Text: "100% BUTTER"}, 1},
		{LogFilter{Text: "100_"}, 0},    // LIKE wildcards are taken literally
		{LogFilter{Text: "title"}, 0},   // field names aren't content
		{LogFilter{Text: "turkey?"}, 0}, // nor are passwords
	} {
		if got, n, err := store.PostLogs(ctx, c.f, 1); err != nil || n != c.want || len(got) > 1 {
			t.Errorf("PostLogs(%+v) = %d entries of %d, %v; want %d", c.f, len(got), n, err, c.want)
		}
	}

	resp, body := get(t, chris, srv.URL+"/logs?q=pecan")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "with 100% butter") || strings.Contains(body, "Mallory") {
		t.Fatalf("filtered /logs: status %d, body:\n%s", resp.StatusCode, body)
	}
	if _, body := get(t, chris, srv.URL+"/logs"); strings.Contains(body, "turkey?") || strings.Contains(body, "gobble") {
		t.Fatal("/logs shows a password")
	}
}

func TestClientIP(t *testing.T) {
	for _, c := range []struct {
		name   string
		onFly  bool
		remote string
		header string // header: value
		want   string
	}{
		{"direct", false, "198.51.100.7:5123", "", "198.51.100.7"},
		{"fly", true, "172.16.3.2:4000", "Fly-Client-IP: 203.0.113.9", "203.0.113.9"},
		{"fly header off fly", false, "198.51.100.7:5123", "Fly-Client-IP: 203.0.113.9", "198.51.100.7"},
		{"local proxy", false, "127.0.0.1:4000", "X-Forwarded-For: 10.0.0.1, 203.0.113.9", "203.0.113.9"},
		{"forwarded by stranger", false, "198.51.100.7:5123", "X-Forwarded-For: 203.0.113.9", "198.51.100.7"},
		{"mapped ipv4", false, "[::ffff:198.51.100.7]:5123", "", "198.51.100.7"},
		{"ipv6", false, "[2001:db8::1]:5123", "", "2001:db8::1"},
	} {
		a := &app{cfg: config{OnFly: c.onFly}}
		r := httptest.NewRequest("POST", "/forum", nil)
		r.RemoteAddr = c.remote
		if k, v, ok := strings.Cut(c.header, ": "); ok {
			r.Header.Set(k, v)
		}
		if got := a.clientIP(r); got != c.want {
			t.Errorf("%s: clientIP = %q, want %q", c.name, got, c.want)
		}
	}
}
