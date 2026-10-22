package main

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

// guestList is the RSVP picture for the event, from the current user's seat.
type guestList struct {
	Going    []RSVP
	Declined []RSVP
	Mine     *RSVP // nil until the current user answers
	Total    int   // everyone coming, guests included
	Kids     int
}

func (g guestList) Adults() int { return g.Total - g.Kids }

func buildGuestList(rsvps []RSVP, user string) guestList {
	var g guestList
	for i, r := range rsvps {
		if sameName(r.Name, user) {
			g.Mine = &rsvps[i]
		}
		if !r.Going {
			g.Declined = append(g.Declined, r)
			continue
		}
		g.Going = append(g.Going, r)
		g.Total += r.Size()
		for _, guest := range r.Guests {
			if guest.Kid {
				g.Kids++
			}
		}
	}
	return g
}

type guestForm struct {
	Name  string
	Kid   bool
	Error string
}

type detailsPage struct {
	Menu   menu
	Guests guestList
	Form   guestForm
}

func (a *app) details(w http.ResponseWriter, r *http.Request) {
	a.renderDetails(w, r, http.StatusOK, guestForm{})
}

func (a *app) renderDetails(w http.ResponseWriter, r *http.Request, status int, form guestForm) {
	ctx := r.Context()
	dishes, err := a.store.ListDishes(ctx)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	rsvps, err := a.store.ListRSVPs(ctx)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.render(w, r, status, "details", view{Title: "Event Details", Nav: "details", Page: detailsPage{
		Menu:   buildMenu(dishes),
		Guests: buildGuestList(rsvps, userName(r)),
		Form:   form,
	}})
}

func (a *app) setRSVP(w http.ResponseWriter, r *http.Request) {
	var going bool
	switch r.FormValue("going") {
	case "yes":
		going = true
	case "no":
	default:
		http.Redirect(w, r, "/details#rsvp", http.StatusSeeOther)
		return
	}
	name := userName(r)
	if err := a.store.SetRSVP(r.Context(), name, going); err != nil {
		a.serverError(w, r, err)
		return
	}
	if going {
		setFlash(w, r, "You're in, "+name+"! See you at the table.")
	} else {
		setFlash(w, r, "Sorry you can't make it, "+name+". We'll miss you!")
	}
	http.Redirect(w, r, "/details#rsvp", http.StatusSeeOther)
}

func (a *app) addGuest(w http.ResponseWriter, r *http.Request) {
	f := guestForm{
		Name: strings.Join(strings.Fields(r.FormValue("name")), " "),
		Kid:  r.FormValue("age") == "kid",
	}
	switch n := utf8.RuneCountInString(f.Name); {
	case n == 0:
		f.Error = "Who are you bringing?"
	case n > maxNameLen:
		f.Error = "Keep names under 40 characters."
	}
	if f.Error != "" {
		a.renderDetails(w, r, http.StatusUnprocessableEntity, f)
		return
	}
	if _, err := a.store.AddGuest(r.Context(), userName(r), Guest{Name: f.Name, Kid: f.Kid}); err != nil {
		a.serverError(w, r, err)
		return
	}
	setFlash(w, r, f.Name+" is on the guest list!")
	http.Redirect(w, r, "/details#rsvp", http.StatusSeeOther)
}

func (a *app) deleteGuest(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		a.notFound(w, r)
		return
	}
	g, err := a.store.GetGuest(r.Context(), id)
	if errors.Is(err, errNotFound) {
		a.notFound(w, r)
		return
	}
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if !sameName(g.By, userName(r)) {
		setFlash(w, r, "Only "+g.By+" can take "+g.Name+" off the list.")
		http.Redirect(w, r, "/details#guests", http.StatusSeeOther)
		return
	}
	if err := a.store.DeleteGuest(r.Context(), g.ID); err != nil {
		a.serverError(w, r, err)
		return
	}
	setFlash(w, r, g.Name+" is off the guest list.")
	http.Redirect(w, r, "/details#rsvp", http.StatusSeeOther)
}
