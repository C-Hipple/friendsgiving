package main

import "net/http"

type homePage struct {
	Menu    menu
	Mine    []Dish
	Threads []Thread
	Guests  guestList
}

func (a *app) home(w http.ResponseWriter, r *http.Request) {
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
	a.render(w, r, http.StatusOK, "home", view{Nav: "home", Page: homePage{
		Menu:    buildMenu(dishes),
		Mine:    dishesBy(dishes, userName(r)),
		Threads: threads,
		Guests:  buildGuestList(rsvps, userName(r)),
	}})
}
