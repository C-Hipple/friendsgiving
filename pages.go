package main

import "net/http"

type homePage struct {
	Menu    menu
	Dishes  []Dish
	Threads []Thread
	Guests  guestList
	Form    guestForm // add-a-guest form, holding a rejected entry
}

// Mine is what the current user is bringing.
func (p homePage) Mine(user string) []Dish { return dishesBy(p.Dishes, user) }

// Bringing is what someone on the guest list is bringing.
func (p homePage) Bringing(name string) []Dish { return dishesBy(p.Dishes, name) }

func (a *app) home(w http.ResponseWriter, r *http.Request) {
	a.renderHome(w, r, http.StatusOK, guestForm{})
}

func (a *app) renderHome(w http.ResponseWriter, r *http.Request, status int, form guestForm) {
	ctx := r.Context()
	dishes, err := a.store.ListDishes(ctx)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	threads, err := a.store.ListThreads(ctx, 4)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	rsvps, err := a.store.ListRSVPs(ctx)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.render(w, r, status, "home", view{Nav: "home", Page: homePage{
		Menu:    buildMenu(dishes),
		Dishes:  dishes,
		Threads: threads,
		Guests:  buildGuestList(rsvps, userName(r)),
		Form:    form,
	}})
}

type detailsPage struct {
	Menu   menu
	Guests guestList
}

func (a *app) details(w http.ResponseWriter, r *http.Request) {
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
	a.render(w, r, http.StatusOK, "details", view{Title: "Event FAQ", Nav: "details", Page: detailsPage{
		Menu:   buildMenu(dishes),
		Guests: buildGuestList(rsvps, userName(r)),
	}})
}
