package main

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxPostLen = 5000

type threadForm struct {
	Title  string
	Body   string
	Errors map[string]string
}

type forumPage struct {
	Threads []Thread
	People  []Person
	Menu    menu
	Form    threadForm
}

func (a *app) forum(w http.ResponseWriter, r *http.Request) {
	a.renderForum(w, r, http.StatusOK, threadForm{})
}

func (a *app) renderForum(w http.ResponseWriter, r *http.Request, status int, form threadForm) {
	ctx := r.Context()
	threads, err := a.store.ListThreads(ctx, 0)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	people, err := a.store.People(ctx)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	dishes, err := a.store.ListDishes(ctx)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.render(w, r, status, "forum", view{Title: "Forum", Nav: "forum",
		Page: forumPage{Threads: threads, People: people, Menu: buildMenu(dishes), Form: form}})
}

func postBody(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
}

func (a *app) createThread(w http.ResponseWriter, r *http.Request) {
	f := threadForm{
		Title:  strings.Join(strings.Fields(r.FormValue("title")), " "),
		Body:   postBody(r.FormValue("body")),
		Errors: map[string]string{},
	}
	switch n := utf8.RuneCountInString(f.Title); {
	case n == 0:
		f.Errors["title"] = "Give your thread a title."
	case n > 120:
		f.Errors["title"] = "Keep the title under 120 characters."
	}
	if utf8.RuneCountInString(f.Body) > maxPostLen {
		f.Errors["body"] = "That's a lot of words. Keep it under 5,000 characters."
	}
	if len(f.Errors) > 0 {
		a.renderForum(w, r, http.StatusUnprocessableEntity, f)
		return
	}
	id, err := a.store.CreateThread(r.Context(), Thread{Title: f.Title, Body: f.Body, Author: userName(r)})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	setFlash(w, r, "Thread posted!")
	http.Redirect(w, r, "/forum/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

type threadPage struct {
	Thread   Thread
	Replies  []Reply
	Reply    string
	ReplyErr string
}

// loadThread fetches the thread named in the URL, writing a 404 or 500 and
// returning false when it can't.
func (a *app) loadThread(w http.ResponseWriter, r *http.Request) (Thread, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		a.notFound(w, r)
		return Thread{}, false
	}
	t, err := a.store.GetThread(r.Context(), id)
	if errors.Is(err, errNotFound) {
		a.notFound(w, r)
		return t, false
	}
	if err != nil {
		a.serverError(w, r, err)
		return t, false
	}
	return t, true
}

func (a *app) thread(w http.ResponseWriter, r *http.Request) {
	t, ok := a.loadThread(w, r)
	if !ok {
		return
	}
	a.renderThread(w, r, http.StatusOK, threadPage{Thread: t})
}

func (a *app) renderThread(w http.ResponseWriter, r *http.Request, status int, p threadPage) {
	replies, err := a.store.ListReplies(r.Context(), p.Thread.ID)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	p.Replies = replies
	a.render(w, r, status, "thread", view{Title: p.Thread.Title, Nav: "forum", Page: p})
}

func (a *app) createReply(w http.ResponseWriter, r *http.Request) {
	t, ok := a.loadThread(w, r)
	if !ok {
		return
	}
	body := postBody(r.FormValue("body"))
	switch n := utf8.RuneCountInString(body); {
	case n == 0:
		a.renderThread(w, r, http.StatusUnprocessableEntity, threadPage{Thread: t, ReplyErr: "Write something first."})
		return
	case n > maxPostLen:
		a.renderThread(w, r, http.StatusUnprocessableEntity, threadPage{Thread: t, Reply: body,
			ReplyErr: "That's a lot of words. Keep it under 5,000 characters."})
		return
	}
	if err := a.store.CreateReply(r.Context(), Reply{ThreadID: t.ID, Body: body, Author: userName(r)}); err != nil {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/forum/"+strconv.FormatInt(t.ID, 10)+"#latest", http.StatusSeeOther)
}

func (a *app) deleteThread(w http.ResponseWriter, r *http.Request) {
	t, ok := a.loadThread(w, r)
	if !ok {
		return
	}
	if !sameName(t.Author, userName(r)) {
		setFlash(w, r, "Only "+t.Author+" can delete this thread.")
		http.Redirect(w, r, "/forum/"+strconv.FormatInt(t.ID, 10), http.StatusSeeOther)
		return
	}
	if err := a.store.DeleteThread(r.Context(), t.ID); err != nil {
		a.serverError(w, r, err)
		return
	}
	setFlash(w, r, "Thread deleted.")
	http.Redirect(w, r, "/forum", http.StatusSeeOther)
}

func (a *app) deleteReply(w http.ResponseWriter, r *http.Request) {
	t, ok := a.loadThread(w, r)
	if !ok {
		return
	}
	back := "/forum/" + strconv.FormatInt(t.ID, 10)
	rid, err := strconv.ParseInt(r.PathValue("rid"), 10, 64)
	if err != nil {
		a.notFound(w, r)
		return
	}
	reply, err := a.store.GetReply(r.Context(), rid)
	if err != nil && !errors.Is(err, errNotFound) {
		a.serverError(w, r, err)
		return
	}
	if err != nil || reply.ThreadID != t.ID {
		a.notFound(w, r)
		return
	}
	if !sameName(reply.Author, userName(r)) {
		setFlash(w, r, "Only "+reply.Author+" can delete that reply.")
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	if err := a.store.DeleteReply(r.Context(), reply); err != nil {
		a.serverError(w, r, err)
		return
	}
	setFlash(w, r, "Reply deleted.")
	http.Redirect(w, r, back, http.StatusSeeOther)
}
