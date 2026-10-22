package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// /mcp serves the Model Context Protocol over its Streamable HTTP transport,
// so friends can hand the site to Claude. It's stateless: each POST carries
// one JSON-RPC message and gets one JSON reply, never a stream or a session.
//
// Instead of the cookie, it takes the shared password as HTTP Basic auth
// with any name as the username, or as name and password query parameters
// for apps that only take a URL. That name is who it posts as, under the same
// ownership rules as the web pages.

// mcpVersions are the protocol revisions spoken here, newest first. The part
// this server uses, tools with text results, is the same in all of them.
var mcpVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

type rpcRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (a *app) mcp(w http.ResponseWriter, r *http.Request) {
	// Guards against DNS rebinding, as the spec asks. Real clients aren't
	// browsers and send no Origin.
	if o := r.Header.Get("Origin"); o != "" {
		if u, err := url.Parse(o); err != nil || u.Host != r.Host {
			http.Error(w, "Cross-origin requests aren't allowed.", http.StatusForbidden)
			return
		}
	}
	if r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html") {
		http.Redirect(w, r, "/mcp/docs", http.StatusSeeOther)
		return
	}
	if r.Method != http.MethodPost {
		// Stateless: no server-sent stream to GET, no session to DELETE.
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "Send MCP messages with POST.", http.StatusMethodNotAllowed)
		return
	}
	user, err := a.mcpUser(r)
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Basic realm="brogiving", charset="UTF-8"`)
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeRPC(w, rpcResponse{ID: json.RawMessage("null"),
			Error: &rpcError{-32700, "Parse error: send one JSON-RPC message per request."}})
		return
	}
	if len(req.ID) == 0 || req.Method == "" {
		// A notification, or a response to a request we never send.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	result, rpcErr := a.mcpHandle(r.Context(), user, req)
	writeRPC(w, rpcResponse{ID: req.ID, Result: result, Error: rpcErr})
}

// mcpUser checks a request's credentials and returns the name it acts as.
func (a *app) mcpUser(r *http.Request) (string, error) {
	name, password, ok := r.BasicAuth()
	if !ok {
		q := r.URL.Query()
		name, password = q.Get("name"), q.Get("password")
	}
	if !a.passwordOK(password) {
		if password != "" {
			time.Sleep(500 * time.Millisecond) // the login form's speed bump
		}
		return "", errors.New("Wrong or missing password. Use HTTP Basic auth, with your name as the username and the site password as the password, or add ?name=...&password=... to the URL.")
	}
	if name = cleanName(name); name == "" {
		return "", errors.New("Pick a name to post as: it goes in the username, or in ?name=...")
	}
	return name, nil
}

func writeRPC(w http.ResponseWriter, resp rpcResponse) {
	resp.JSONRPC = "2.0"
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(resp)
}

func (a *app) mcpHandle(ctx context.Context, user string, req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p)
		version := mcpVersions[0]
		if slices.Contains(mcpVersions, p.ProtocolVersion) {
			version = p.ProtocolVersion
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]string{"name": "brogiving", "title": a.cfg.Event.Name, "version": "1.0.0"},
			"instructions":    a.mcpInstructions(user),
		}, nil
	case "ping":
		return struct{}{}, nil
	case "tools/list":
		return map[string]any{"tools": mcpTools}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{-32602, "Invalid params: " + err.Error()}
		}
		i := slices.IndexFunc(mcpTools, func(t mcpTool) bool { return t.Name == p.Name })
		if i < 0 {
			return nil, &rpcError{-32602, "Unknown tool: " + p.Name}
		}
		log.Printf("mcp: %s called %s", user, p.Name)
		return a.callTool(ctx, &mcpTools[i], user, p.Arguments), nil
	}
	return nil, &rpcError{-32601, "Method not found: " + req.Method}
}

func (a *app) mcpInstructions(user string) string {
	return fmt.Sprintf(`This is the private coordination site for %[1]s, a Thanksgiving potluck among friends: a sign-up sheet for dishes, the event details, and a small forum.

You're signed in as %[2]s. Dishes you add and posts you write appear under that name for everyone to see, and you can only change or delete things under it. Write in your user's voice, and check with them before you post or sign them up for anything.

Start with get_event for the plan and which courses still need someone. Check list_dishes before adding a dish so nobody doubles up, and set needs_oven for anything that has to bake or reheat at the party: oven space is the bottleneck.`,
		a.cfg.Event.Name, user)
}

// toolErr is a problem the caller can fix. Its text goes back to the model.
type toolErr string

func (e toolErr) Error() string { return string(e) }

func (a *app) callTool(ctx context.Context, t *mcpTool, user string, args json.RawMessage) map[string]any {
	out, err := t.run(a, ctx, user, args)
	var text bytes.Buffer
	if err == nil {
		enc := json.NewEncoder(&text)
		enc.SetEscapeHTML(false) // "Sides & Trimmings", not "Sides & Trimmings"
		enc.SetIndent("", "  ")
		err = enc.Encode(out)
	}
	var msg toolErr
	switch {
	case errors.As(err, &msg):
		return toolResult(string(msg), true)
	case err != nil:
		log.Printf("error: mcp %s: %v", t.Name, err)
		return toolResult("Something burned in the kitchen. Try again in a moment.", true)
	}
	return toolResult(strings.TrimSpace(text.String()), false)
}

func toolResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]string{{"type": "text", "text": text}},
		"isError": isError,
	}
}

type mcpTool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations toolHints      `json:"annotations"`
	run         func(a *app, ctx context.Context, user string, args json.RawMessage) (any, error)
}

// toolHints tell clients which tools are safe to run without asking.
type toolHints struct {
	ReadOnly    bool `json:"readOnlyHint"`
	Destructive bool `json:"destructiveHint"`
	Idempotent  bool `json:"idempotentHint"`
	OpenWorld   bool `json:"openWorldHint"`
}

var (
	reads   = toolHints{ReadOnly: true}
	adds    = toolHints{}
	changes = toolHints{Destructive: true, Idempotent: true}
)

var mcpTools = []mcpTool{
	{
		Name: "get_event", Title: "Event details", Annotations: reads, run: (*app).toolGetEvent,
		Description: "The plan: date, time and place, plus how the potluck is shaping up (dishes per course, how many need the oven) and how busy the forum is. Start here.",
		InputSchema: object(nil, map[string]any{}),
	},
	{
		Name: "list_dishes", Title: "List potluck dishes", Annotations: reads, run: (*app).toolListDishes,
		Description: "Everything people have signed up to bring, oldest first. Optionally narrow it to one course, one person, or what needs the oven.",
		InputSchema: object(nil, map[string]any{
			"course":     oneOf("Only this course.", courseKeys()),
			"brought_by": prop("string", "Only dishes this person is bringing."),
			"needs_oven": prop("boolean", "true for only the dishes that need the oven, false for only the ones that don't."),
		}),
	},
	{
		Name: "add_dish", Title: "Sign up for a dish", Annotations: adds, run: (*app).toolAddDish,
		Description: "Put a dish on the menu under your name. Check list_dishes first so nobody doubles up. Oven space is the bottleneck, so set needs_oven if it has to bake or reheat at the party, with the temperature and time in oven_notes.",
		InputSchema: object([]string{"name", "course"}, dishProps()),
	},
	{
		Name: "update_dish", Title: "Change a dish", Annotations: changes, run: (*app).toolUpdateDish,
		Description: "Change a dish you're bringing. Only the fields you pass change. You can only change your own dishes.",
		InputSchema: object([]string{"id"}, withID("The dish's id, from list_dishes.", dishProps())),
	},
	{
		Name: "remove_dish", Title: "Remove a dish", Annotations: changes, run: (*app).toolRemoveDish,
		Description: "Take one of your dishes off the menu.",
		InputSchema: object([]string{"id"}, withID("The dish's id, from list_dishes.", map[string]any{})),
	},
	{
		Name: "list_threads", Title: "List forum threads", Annotations: reads, run: (*app).toolListThreads,
		Description: "Forum threads with their opening posts, most recently active first. Use read_thread for the replies.",
		InputSchema: object(nil, map[string]any{}),
	},
	{
		Name: "read_thread", Title: "Read a thread", Annotations: reads, run: (*app).toolReadThread,
		Description: "A forum thread and all of its replies, oldest first.",
		InputSchema: object([]string{"id"}, withID("The thread's id, from list_threads.", map[string]any{})),
	},
	{
		Name: "start_thread", Title: "Start a thread", Annotations: adds, run: (*app).toolStartThread,
		Description: "Start a forum thread under your name: carpools, recipes, games, questions for the host. Check list_threads first in case one already covers it.",
		InputSchema: object([]string{"title"}, map[string]any{
			"title": prop("string", "Up to 120 characters."),
			"body":  prop("string", "Plain text, up to 5,000 characters. Line breaks are kept."),
		}),
	},
	{
		Name: "reply_to_thread", Title: "Reply to a thread", Annotations: adds, run: (*app).toolReplyToThread,
		Description: "Reply to a forum thread under your name.",
		InputSchema: object([]string{"thread_id", "body"}, map[string]any{
			"thread_id": prop("integer", "The thread's id, from list_threads."),
			"body":      prop("string", "Plain text, up to 5,000 characters. Line breaks are kept."),
		}),
	},
	{
		Name: "delete_thread", Title: "Delete a thread", Annotations: changes, run: (*app).toolDeleteThread,
		Description: "Delete a thread you started, along with all of its replies.",
		InputSchema: object([]string{"id"}, withID("The thread's id, from list_threads.", map[string]any{})),
	},
	{
		Name: "delete_reply", Title: "Delete a reply", Annotations: changes, run: (*app).toolDeleteReply,
		Description: "Delete one of your forum replies.",
		InputSchema: object([]string{"id"}, withID("The reply's id, from read_thread.", map[string]any{})),
	},
	{
		Name: "list_people", Title: "List people", Annotations: reads, run: (*app).toolListPeople,
		Description: "Everyone who has signed up for a dish or posted in the forum, with how many of each, busiest first.",
		InputSchema: object(nil, map[string]any{}),
	},
}

// dishProps are add_dish's arguments, which update_dish shares.
func dishProps() map[string]any {
	return map[string]any{
		"name":       prop("string", `What you're bringing, e.g. "Grandma's sweet potato casserole". Up to 80 characters.`),
		"course":     oneOf("Which course it's for.", courseKeys()),
		"needs_oven": prop("boolean", "Whether it has to bake or reheat in the oven at the party."),
		"oven_notes": prop("string", `Temperature and time, e.g. "350°F for 30 min" or "just a reheat". Up to 80 characters. Dropped unless needs_oven is true.`),
		"notes":      prop("string", `Anything else, e.g. "Serves 10. Bringing extra gravy." Up to 280 characters.`),
		"dietary_tags": map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "string", "enum": tagKeys()},
			"description": "Dietary notes that apply. Mark anything with nuts.",
		},
	}
}

func object(required []string, props map[string]any) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if required != nil {
		s["required"] = required
	}
	return s
}

func prop(typ, desc string) map[string]any {
	return map[string]any{"type": typ, "description": desc}
}

func oneOf(desc string, values []string) map[string]any {
	return map[string]any{"type": "string", "enum": values, "description": desc}
}

func withID(desc string, props map[string]any) map[string]any {
	props["id"] = prop("integer", desc)
	return props
}

func courseKeys() []string {
	var keys []string
	for _, c := range categories {
		keys = append(keys, c.Key)
	}
	return keys
}

func tagKeys() []string {
	var keys []string
	for _, t := range dietTags {
		keys = append(keys, t.Key)
	}
	return keys
}

func decodeArgs(args json.RawMessage, v any) error {
	if len(args) == 0 {
		return nil
	}
	if err := json.Unmarshal(args, v); err != nil {
		return toolErr("Couldn't read the arguments: " + err.Error())
	}
	return nil
}

func argID(args json.RawMessage) (int64, error) {
	var in struct {
		ID int64 `json:"id"`
	}
	err := decodeArgs(args, &in)
	return in.ID, err
}

// problems turns form errors into one message for the model.
func problems(errs map[string]string) error {
	if len(errs) == 0 {
		return nil
	}
	var msgs []string
	for _, k := range slices.Sorted(maps.Keys(errs)) {
		msgs = append(msgs, errs[k])
	}
	return toolErr(strings.Join(msgs, " "))
}

// orEmpty keeps empty lists as [] rather than null in tool results.
func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func (a *app) toolGetEvent(ctx context.Context, user string, _ json.RawMessage) (any, error) {
	dishes, err := a.store.ListDishes(ctx)
	if err != nil {
		return nil, err
	}
	c, err := a.store.Counts(ctx)
	if err != nil {
		return nil, err
	}
	m := buildMenu(dishes)
	type courseStatus struct {
		Course string `json:"course"`
		Title  string `json:"title"`
		Dishes int    `json:"dishes"`
	}
	var courses []courseStatus
	for _, c := range m.Courses {
		courses = append(courses, courseStatus{c.Key, c.Title, len(c.Dishes)})
	}
	return map[string]any{
		"signed_in_as": user,
		"event":        a.cfg.Event,
		"potluck": map[string]any{
			"dishes":     m.Total,
			"cooks":      m.Cooks,
			"needs_oven": len(m.Oven),
			"courses":    courses,
		},
		"forum": map[string]any{"threads": c.Threads, "replies": c.Replies},
	}, nil
}

func (a *app) toolListDishes(ctx context.Context, _ string, args json.RawMessage) (any, error) {
	var in struct {
		Course    string `json:"course"`
		BroughtBy string `json:"brought_by"`
		NeedsOven *bool  `json:"needs_oven"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return nil, err
	}
	if in.Course != "" && categoryByKey(in.Course) == nil {
		return nil, toolErr(fmt.Sprintf("There's no %q course. Pick one of %s.", in.Course, strings.Join(courseKeys(), ", ")))
	}
	dishes, err := a.store.ListDishes(ctx)
	if err != nil {
		return nil, err
	}
	out := []Dish{}
	for _, d := range dishes {
		if (in.Course == "" || d.Category == in.Course) &&
			(in.BroughtBy == "" || sameName(d.BroughtBy, in.BroughtBy)) &&
			(in.NeedsOven == nil || d.NeedsOven == *in.NeedsOven) {
			out = append(out, d)
		}
	}
	return map[string]any{"dishes": out}, nil
}

func (a *app) toolAddDish(ctx context.Context, user string, args json.RawMessage) (any, error) {
	var d Dish
	if err := decodeArgs(args, &d); err != nil {
		return nil, err
	}
	d.BroughtBy = user
	if err := problems(checkDish(&d)); err != nil {
		return nil, err
	}
	id, err := a.store.CreateDish(ctx, d)
	if err != nil {
		return nil, err
	}
	return a.store.GetDish(ctx, id)
}

// myDish loads the dish whose id is in args, if it's the user's to change.
func (a *app) myDish(ctx context.Context, user string, args json.RawMessage) (Dish, error) {
	id, err := argID(args)
	if err != nil {
		return Dish{}, err
	}
	d, err := a.store.GetDish(ctx, id)
	if errors.Is(err, errNotFound) {
		return d, toolErr(fmt.Sprintf("There's no dish %d. list_dishes shows the ones there are.", id))
	}
	if err != nil {
		return d, err
	}
	if !sameName(d.BroughtBy, user) {
		return d, toolErr("Only " + d.BroughtBy + " can change " + d.Name + ".")
	}
	return d, nil
}

func (a *app) toolUpdateDish(ctx context.Context, user string, args json.RawMessage) (any, error) {
	d, err := a.myDish(ctx, user, args)
	if err != nil {
		return nil, err
	}
	// Decoding over the saved dish leaves out-of-args fields as they were.
	id, by := d.ID, d.BroughtBy
	if err := decodeArgs(args, &d); err != nil {
		return nil, err
	}
	d.ID, d.BroughtBy = id, by
	if err := problems(checkDish(&d)); err != nil {
		return nil, err
	}
	if err := a.store.UpdateDish(ctx, d); err != nil {
		return nil, err
	}
	return a.store.GetDish(ctx, d.ID)
}

func (a *app) toolRemoveDish(ctx context.Context, user string, args json.RawMessage) (any, error) {
	d, err := a.myDish(ctx, user, args)
	if err != nil {
		return nil, err
	}
	if err := a.store.DeleteDish(ctx, d.ID); err != nil {
		return nil, err
	}
	return map[string]any{"removed": d}, nil
}

func (a *app) toolListThreads(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
	threads, err := a.store.ListThreads(ctx, 0)
	if err != nil {
		return nil, err
	}
	return map[string]any{"threads": orEmpty(threads)}, nil
}

func (a *app) findThread(ctx context.Context, id int64) (Thread, error) {
	t, err := a.store.GetThread(ctx, id)
	if errors.Is(err, errNotFound) {
		return t, toolErr(fmt.Sprintf("There's no thread %d. list_threads shows the ones there are.", id))
	}
	return t, err
}

func (a *app) toolReadThread(ctx context.Context, _ string, args json.RawMessage) (any, error) {
	id, err := argID(args)
	if err != nil {
		return nil, err
	}
	t, err := a.findThread(ctx, id)
	if err != nil {
		return nil, err
	}
	replies, err := a.store.ListReplies(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"thread": t, "replies": orEmpty(replies)}, nil
}

func (a *app) toolStartThread(ctx context.Context, user string, args json.RawMessage) (any, error) {
	var in struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return nil, err
	}
	f := newThreadForm(in.Title, in.Body)
	if err := problems(f.Errors); err != nil {
		return nil, err
	}
	id, err := a.store.CreateThread(ctx, Thread{Title: f.Title, Body: f.Body, Author: user})
	if err != nil {
		return nil, err
	}
	return a.store.GetThread(ctx, id)
}

func (a *app) toolReplyToThread(ctx context.Context, user string, args json.RawMessage) (any, error) {
	var in struct {
		ThreadID int64  `json:"thread_id"`
		Body     string `json:"body"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return nil, err
	}
	t, err := a.findThread(ctx, in.ThreadID)
	if err != nil {
		return nil, err
	}
	body := postBody(in.Body)
	if msg := replyError(body); msg != "" {
		return nil, toolErr(msg)
	}
	id, err := a.store.CreateReply(ctx, Reply{ThreadID: t.ID, Body: body, Author: user})
	if err != nil {
		return nil, err
	}
	return a.store.GetReply(ctx, id)
}

func (a *app) toolDeleteThread(ctx context.Context, user string, args json.RawMessage) (any, error) {
	id, err := argID(args)
	if err != nil {
		return nil, err
	}
	t, err := a.findThread(ctx, id)
	if err != nil {
		return nil, err
	}
	if !sameName(t.Author, user) {
		return nil, toolErr("Only " + t.Author + " can delete this thread.")
	}
	if err := a.store.DeleteThread(ctx, t.ID); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": t}, nil
}

func (a *app) toolDeleteReply(ctx context.Context, user string, args json.RawMessage) (any, error) {
	id, err := argID(args)
	if err != nil {
		return nil, err
	}
	reply, err := a.store.GetReply(ctx, id)
	if errors.Is(err, errNotFound) {
		return nil, toolErr(fmt.Sprintf("There's no reply %d. read_thread shows a thread's replies.", id))
	}
	if err != nil {
		return nil, err
	}
	if !sameName(reply.Author, user) {
		return nil, toolErr("Only " + reply.Author + " can delete that reply.")
	}
	if err := a.store.DeleteReply(ctx, reply); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": reply}, nil
}

func (a *app) toolListPeople(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
	people, err := a.store.People(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"people": orEmpty(people)}, nil
}

type mcpDocsPage struct {
	URL   string // the /mcp endpoint, as this visitor reached the site
	Name  string // the visitor's name, escaped for a query string
	Tools []mcpTool
}

func (a *app) mcpDocs(w http.ResponseWriter, r *http.Request) {
	scheme := "http"
	if overHTTPS(r) {
		scheme = "https"
	}
	a.render(w, r, http.StatusOK, "mcp", view{Title: "Bring your Claude", Page: mcpDocsPage{
		URL:   scheme + "://" + r.Host + "/mcp",
		Name:  url.QueryEscape(userName(r)),
		Tools: mcpTools,
	}})
}
