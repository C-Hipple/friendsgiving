package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// mcpClient sends MCP messages as one person, with Basic auth.
type mcpClient struct {
	t    *testing.T
	url  string
	name string
	pass string
}

func (c mcpClient) post(body any, header ...string) *http.Response {
	c.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, c.url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if c.name != "" || c.pass != "" {
		req.SetBasicAuth(c.name, c.pass)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := guest(c.t).Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	return resp
}

type rpcReply struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

func (c mcpClient) rpc(method string, params any) rpcReply {
	c.t.Helper()
	resp := c.post(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		c.t.Fatalf("%s: status %d", method, resp.StatusCode)
	}
	var r rpcReply
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		c.t.Fatal(err)
	}
	return r
}

// call runs a tool and returns its text and whether it reported an error.
func (c mcpClient) call(tool string, args map[string]any) (string, bool) {
	c.t.Helper()
	r := c.rpc("tools/call", map[string]any{"name": tool, "arguments": args})
	if r.Error != nil {
		c.t.Fatalf("%s: %s", tool, r.Error.Message)
	}
	var res struct {
		Content []struct{ Text string } `json:"content"`
		IsError bool                    `json:"isError"`
	}
	if err := json.Unmarshal(r.Result, &res); err != nil || len(res.Content) != 1 {
		c.t.Fatalf("%s: result %s", tool, r.Result)
	}
	return res.Content[0].Text, res.IsError
}

func TestMCPAuth(t *testing.T) {
	srv, _ := newTestServer(t)
	hello := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}}

	for _, c := range []mcpClient{
		{t: t, url: srv.URL + "/mcp"},
		{t: t, url: srv.URL + "/mcp", name: "Mallory", pass: "wrong"},
		{t: t, url: srv.URL + "/mcp", name: "  ", pass: "gobble"},
		{t: t, url: srv.URL + "/mcp?name=Mallory&password=wrong"},
	} {
		resp := c.post(hello)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized || !strings.HasPrefix(resp.Header.Get("WWW-Authenticate"), "Basic") {
			t.Errorf("name %q password %q at %s: got %d, want 401 with a Basic challenge", c.name, c.pass, c.url, resp.StatusCode)
		}
	}

	// The query-string form, for apps that only take a URL.
	q := mcpClient{t: t, url: srv.URL + "/mcp?" + url.Values{"name": {"Big Mike"}, "password": {"gobble"}}.Encode()}
	if text, _ := q.call("get_event", nil); !strings.Contains(text, `"signed_in_as": "Big Mike"`) {
		t.Errorf("query auth: get_event said %s", text)
	}

	ok := mcpClient{t: t, url: srv.URL + "/mcp", name: "Chris", pass: "gobble"}
	resp := ok.post(hello, "Origin", "https://evil.example")
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin: got %d, want 403", resp.StatusCode)
	}

	// Other methods get a 405, never the site's pages, even without a password.
	for _, method := range []string{http.MethodGet, http.MethodDelete, http.MethodPut} {
		req, _ := http.NewRequest(method, srv.URL+"/mcp", nil)
		resp, err := guest(t).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s /mcp: got %d, want 405", method, resp.StatusCode)
		}
	}
	// Browsers get sent to the docs, which are behind the usual gate.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/mcp", nil)
	req.Header.Set("Accept", "text/html")
	resp, _ = guest(t).Do(req)
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); loc != "/mcp/docs" {
		t.Errorf("browser GET /mcp: Location %q, want /mcp/docs", loc)
	}
	if resp, _ := get(t, guest(t), srv.URL+"/mcp/docs"); !strings.HasPrefix(resp.Header.Get("Location"), "/login") {
		t.Errorf("docs without a cookie: Location %q, want /login", resp.Header.Get("Location"))
	}
}

func TestMCPProtocol(t *testing.T) {
	srv, _ := newTestServer(t)
	c := mcpClient{t: t, url: srv.URL + "/mcp", name: "Chris", pass: "gobble"}

	var info struct {
		ProtocolVersion string `json:"protocolVersion"`
		Instructions    string `json:"instructions"`
	}
	json.Unmarshal(c.rpc("initialize", map[string]any{"protocolVersion": "2025-06-18"}).Result, &info)
	if info.ProtocolVersion != "2025-06-18" || !strings.Contains(info.Instructions, "signed in as Chris") {
		t.Errorf("initialize = %+v", info)
	}
	json.Unmarshal(c.rpc("initialize", map[string]any{"protocolVersion": "1999-01-01"}).Result, &info)
	if info.ProtocolVersion != mcpVersions[0] {
		t.Errorf("unknown version: got %q, want %q", info.ProtocolVersion, mcpVersions[0])
	}

	resp := c.post(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("notification: got %d, want 202", resp.StatusCode)
	}

	var list struct {
		Tools []struct {
			Name        string         `json:"name"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	json.Unmarshal(c.rpc("tools/list", nil).Result, &list)
	if len(list.Tools) != len(mcpTools) || list.Tools[0].InputSchema["type"] != "object" {
		t.Errorf("tools/list = %+v", list)
	}

	if r := c.rpc("tools/call", map[string]any{"name": "launch_turkey"}); r.Error == nil || r.Error.Code != -32602 {
		t.Errorf("unknown tool: got %+v", r)
	}
	if r := c.rpc("resources/list", nil); r.Error == nil || r.Error.Code != -32601 {
		t.Errorf("unknown method: got %+v", r)
	}
}

func TestMCPTools(t *testing.T) {
	srv, store := newTestServer(t)
	ctx := context.Background()
	chris := mcpClient{t: t, url: srv.URL + "/mcp", name: "Chris", pass: "gobble"}
	mike := mcpClient{t: t, url: srv.URL + "/mcp", name: "Mike", pass: "gobble"}

	if text, isErr := chris.call("add_dish", map[string]any{"name": " ", "course": "soup"}); !isErr || !strings.Contains(text, "Pick a course.") {
		t.Errorf("invalid dish: %v %s", isErr, text)
	}
	if _, isErr := chris.call("add_dish", map[string]any{
		"name": "Turkey", "course": "main", "needs_oven": true, "oven_notes": "325°F, 4h",
		"dietary_tags": []string{"gluten-free", "bogus"}, "brought_by": "Mike",
	}); isErr {
		t.Fatal("add_dish failed")
	}
	d, _ := store.GetDish(ctx, 1)
	if d.BroughtBy != "Chris" || !d.NeedsOven || strings.Join(d.Tags, ",") != "gluten-free" {
		t.Fatalf("stored dish = %+v", d)
	}

	if text, isErr := mike.call("update_dish", map[string]any{"id": 1, "name": "Tofurkey"}); !isErr || !strings.Contains(text, "Only Chris") {
		t.Errorf("Mike edited Chris's dish: %v %s", isErr, text)
	}
	if _, isErr := mike.call("remove_dish", map[string]any{"id": 1}); !isErr {
		t.Error("Mike removed Chris's dish")
	}
	// Partial update: only the oven changes, and its notes go with it.
	chris.call("update_dish", map[string]any{"id": 1, "needs_oven": false})
	if d, _ := store.GetDish(ctx, 1); d.Name != "Turkey" || d.NeedsOven || d.OvenNotes != "" || len(d.Tags) != 1 {
		t.Errorf("after partial update: %+v", d)
	}

	if text, _ := mike.call("list_dishes", map[string]any{"course": "main"}); !strings.Contains(text, `"name": "Turkey"`) {
		t.Errorf("list_dishes main: %s", text)
	}
	if text, _ := mike.call("list_dishes", map[string]any{"needs_oven": true}); !strings.Contains(text, `"dishes": []`) {
		t.Errorf("list_dishes oven: %s", text)
	}

	chris.call("start_thread", map[string]any{"title": "Carpool?", "body": "3 seats"})
	mike.call("reply_to_thread", map[string]any{"thread_id": 1, "body": "Me!"})
	if text, _ := chris.call("read_thread", map[string]any{"id": 1}); !strings.Contains(text, `"author": "Mike"`) {
		t.Errorf("read_thread: %s", text)
	}
	if _, isErr := chris.call("delete_reply", map[string]any{"id": 1}); !isErr {
		t.Error("Chris deleted Mike's reply")
	}
	if _, isErr := mike.call("delete_thread", map[string]any{"id": 1}); !isErr {
		t.Error("Mike deleted Chris's thread")
	}
	mike.call("delete_reply", map[string]any{"id": 1})
	if th, _ := store.GetThread(ctx, 1); th.ReplyCount != 0 {
		t.Errorf("reply count after delete = %d", th.ReplyCount)
	}

	if text, _ := chris.call("list_people", nil); !strings.Contains(text, `"name": "Chris"`) {
		t.Errorf("list_people: %s", text)
	}
	if text, _ := chris.call("get_event", nil); !strings.Contains(text, `"dishes": 1`) {
		t.Errorf("get_event: %s", text)
	}
}

func TestMCPRSVP(t *testing.T) {
	srv, store := newTestServer(t)
	ctx := context.Background()
	chris := mcpClient{t: t, url: srv.URL + "/mcp", name: "Chris", pass: "gobble"}
	mike := mcpClient{t: t, url: srv.URL + "/mcp", name: "Mike", pass: "gobble"}

	type listing struct {
		Headcount  map[string]int `json:"headcount"`
		Going      []RSVP         `json:"going"`
		CantMakeIt []string       `json:"cant_make_it"`
		YourRSVP   *RSVP          `json:"your_rsvp"`
	}
	// call runs a tool that should work and decodes its guest list.
	call := func(c mcpClient, tool string, args map[string]any) listing {
		t.Helper()
		text, isErr := c.call(tool, args)
		var g listing
		if err := json.Unmarshal([]byte(text), &g); isErr || err != nil {
			t.Fatalf("%s: %s", tool, text)
		}
		return g
	}

	if g := call(chris, "list_rsvps", nil); g.YourRSVP != nil || g.Headcount["coming"] != 0 || g.Going == nil || g.CantMakeIt == nil {
		t.Errorf("before anyone answers: %+v", g)
	}
	if text, _ := chris.call("get_event", nil); !strings.Contains(text, `"your_answer": "not yet"`) {
		t.Errorf("get_event before an RSVP: %s", text)
	}
	if text, isErr := chris.call("rsvp", map[string]any{}); !isErr || !strings.Contains(text, "going is true or false") {
		t.Errorf("rsvp without an answer: %v %s", isErr, text)
	}
	if text, isErr := chris.call("add_guest", map[string]any{"name": "  "}); !isErr || !strings.Contains(text, "Who are you bringing?") {
		t.Errorf("blank guest: %v %s", isErr, text)
	}

	// Bringing a guest counts you in.
	g := call(chris, "add_guest", map[string]any{"name": " Emma ", "kid": true})
	if g.YourRSVP == nil || !g.YourRSVP.Going || len(g.YourRSVP.Guests) != 1 || g.YourRSVP.Guests[0].Name != "Emma" ||
		g.Headcount["coming"] != 2 || g.Headcount["kids"] != 1 {
		t.Fatalf("after add_guest: %+v", g)
	}
	call(chris, "add_guest", map[string]any{"name": "Jordan"})
	call(mike, "rsvp", map[string]any{"going": false})

	g = call(mike, "list_rsvps", nil)
	if g.Headcount["coming"] != 3 || g.Headcount["adults"] != 2 || len(g.Going) != 1 || len(g.Going[0].Guests) != 2 ||
		strings.Join(g.CantMakeIt, ",") != "Mike" || g.YourRSVP == nil || g.YourRSVP.Going {
		t.Errorf("list_rsvps: %+v", g)
	}

	if text, isErr := mike.call("remove_guest", map[string]any{"id": 1}); !isErr || !strings.Contains(text, "Only Chris can take Emma off the list.") {
		t.Errorf("Mike removed Chris's guest: %v %s", isErr, text)
	}
	if _, isErr := chris.call("remove_guest", map[string]any{"id": 99}); !isErr {
		t.Error("removed a guest who isn't there")
	}

	// Backing out keeps your party on your RSVP, and saying yes brings it back.
	if g := call(chris, "rsvp", map[string]any{"going": false}); g.Headcount["coming"] != 0 || g.YourRSVP == nil || g.YourRSVP.Going || len(g.YourRSVP.Guests) != 2 {
		t.Errorf("after Chris backs out: %+v", g)
	}
	if text, _ := chris.call("get_event", nil); !strings.Contains(text, `"your_answer": "can't make it"`) || !strings.Contains(text, `"cant_make_it": 2`) {
		t.Errorf("get_event after backing out: %s", text)
	}
	if g := call(chris, "rsvp", map[string]any{"going": true}); g.Headcount["coming"] != 3 {
		t.Errorf("after Chris comes back: %+v", g)
	}

	if text, isErr := chris.call("remove_guest", map[string]any{"id": 1}); isErr || !strings.Contains(text, `"name": "Emma"`) {
		t.Errorf("remove_guest: %v %s", isErr, text)
	}
	if _, err := store.GetGuest(ctx, 1); err != errNotFound {
		t.Errorf("after remove_guest: err = %v", err)
	}
	if text, _ := chris.call("get_event", nil); !strings.Contains(text, `"your_answer": "going"`) || !strings.Contains(text, `"coming": 2`) {
		t.Errorf("get_event after removing Emma: %s", text)
	}
}

func TestMCPDocs(t *testing.T) {
	srv, _ := newTestServer(t)
	c := loggedIn(t, srv, "Mike's Claude")

	_, home := get(t, c, srv.URL+"/")
	if !strings.Contains(home, `except with your <a href="/mcp/docs">Claude</a>`) {
		t.Error("footer doesn't link to the MCP docs")
	}
	resp, body := get(t, c, srv.URL+"/mcp/docs")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("docs: status %d", resp.StatusCode)
	}
	// The name is query-escaped, so the link survives shell single quotes.
	if !strings.Contains(body, srv.URL+`/mcp?name=<span data-slot="name">Mike%27s&#43;Claude</span>`) {
		t.Error("docs don't show the connection link")
	}
	for _, tool := range mcpTools {
		if !strings.Contains(body, tool.Name) {
			t.Errorf("docs don't mention %s", tool.Name)
		}
	}
}

func TestMCPPostLog(t *testing.T) {
	srv, store := newTestServer(t)
	ctx := context.Background()
	chris := mcpClient{t: t, url: srv.URL + "/mcp?name=Chris&password=gobble"}
	mike := mcpClient{t: t, url: srv.URL + "/mcp", name: "Mike", pass: "gobble"}

	mallory := mcpClient{t: t, url: srv.URL + "/mcp", name: "Mallory", pass: "turkey?"}
	mallory.post(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"}).Body.Close()
	chris.rpc("initialize", map[string]any{})
	chris.call("add_dish", map[string]any{"name": "Pie", "course": "dessert", "needs_oven": true, "dietary_tags": []string{"nuts"}})
	chris.call("list_dishes", nil) // reads go unlogged, as GETs do on the site
	mike.call("remove_dish", map[string]any{"id": 1})

	all, total, err := store.PostLogs(ctx, LogFilter{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Fatalf("got %d entries, want 3: %+v", total, all)
	}
	if e := all[2]; e.Path != "/mcp" || e.Status != http.StatusUnauthorized || e.User != "" || e.Form.Get("name") != "Mallory" {
		t.Errorf("failed sign-in entry = %+v", e)
	}
	if e := all[1]; e.Path != "/mcp/add_dish" || e.Status != http.StatusOK || e.User != "Chris" || e.IP != "127.0.0.1" ||
		e.Form.Get("name") != "Pie" || e.Form.Get("needs_oven") != "true" || e.Form.Get("dietary_tags") != `["nuts"]` {
		t.Errorf("add_dish entry = %+v", e)
	}
	if e := all[0]; e.Path != "/mcp/remove_dish" || e.Status != http.StatusUnprocessableEntity || e.User != "Mike" || e.Form.Get("id") != "1" {
		t.Errorf("refused remove_dish entry = %+v", e)
	}
	for _, pw := range []string{"gobble", "turkey?"} {
		if _, n, _ := store.PostLogs(ctx, LogFilter{Text: pw}, 1); n != 0 {
			t.Errorf("post log holds the password %q", pw)
		}
	}
}
