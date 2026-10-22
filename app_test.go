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
