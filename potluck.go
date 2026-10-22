package main

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

type category struct {
	Key    string
	Label  string // singular, for form choices
	Plural string // for tabs and counts
	Title  string // section heading
	Blurb  string
	Icon   string
	Tone   string // CSS color family
}

var categories = []category{
	{"appetizer", "Appetizer", "Appetizers", "Appetizers & Grazing", "Something to pick at while the bird rests.", "tapas", "pumpkin"},
	{"main", "Main", "Mains", "The Main Event", "Turkey, ham, and the other centerpieces.", "restaurant", "cranberry"},
	{"side", "Side", "Sides", "Sides & Trimmings", "The true soul of Thanksgiving.", "rice-bowl", "sage"},
	{"dessert", "Dessert", "Desserts", "Sweet Finale", "Pies, crisps, and everything after.", "cake", "amber"},
	{"beverage", "Beverage", "Beverages", "Libations & Beverages", "Cider, wine, beer, and the mocktails.", "local-bar", "rose"},
}

func categoryByKey(key string) *category {
	for i := range categories {
		if categories[i].Key == key {
			return &categories[i]
		}
	}
	return nil
}

type dietTag struct {
	Key   string
	Label string
	Emoji string
	Warn  bool // allergen, not a preference
}

var dietTags = []dietTag{
	{"vegetarian", "Vegetarian", "🌿", false},
	{"vegan", "Vegan", "🌱", false},
	{"gluten-free", "Gluten-free", "🌾", false},
	{"nuts", "Contains nuts", "🥜", true},
}

func tagByKey(key string) *dietTag {
	for i := range dietTags {
		if dietTags[i].Key == key {
			return &dietTags[i]
		}
	}
	return nil
}

// course is one category's slice of the menu.
type course struct {
	category
	Num    int
	Dishes []Dish
}

// tagCount is how many dishes carry one dietary tag.
type tagCount struct {
	dietTag
	N int
}

type menu struct {
	Courses  []course
	Total    int
	Oven     []Dish
	Cooks    int
	Missing  []category
	Tags     []tagCount
	Friendly int // dishes tagged vegetarian, vegan, or gluten-free
}

func buildMenu(dishes []Dish) menu {
	m := menu{Total: len(dishes)}
	cooks := map[string]bool{}
	for i, c := range categories {
		m.Courses = append(m.Courses, course{category: c, Num: i + 1})
	}
	for _, t := range dietTags {
		m.Tags = append(m.Tags, tagCount{dietTag: t})
	}
	for _, d := range dishes {
		for i := range m.Courses {
			if m.Courses[i].Key == d.Category {
				m.Courses[i].Dishes = append(m.Courses[i].Dishes, d)
			}
		}
		if d.NeedsOven {
			m.Oven = append(m.Oven, d)
		}
		cooks[strings.ToLower(d.BroughtBy)] = true
		friendly := false
		for i := range m.Tags {
			if slices.Contains(d.Tags, m.Tags[i].Key) {
				m.Tags[i].N++
				friendly = friendly || !m.Tags[i].Warn
			}
		}
		if friendly {
			m.Friendly++
		}
	}
	for _, c := range m.Courses {
		if len(c.Dishes) == 0 {
			m.Missing = append(m.Missing, c.category)
		}
	}
	m.Cooks = len(cooks)
	return m
}

func (m menu) Covered() int { return len(m.Courses) - len(m.Missing) }

// Tagged reports whether any dish carries a dietary tag.
func (m menu) Tagged() bool {
	for _, t := range m.Tags {
		if t.N > 0 {
			return true
		}
	}
	return false
}

// courseList renders categories as prose: "sides, desserts and beverages".
func courseList(cs []category) string {
	names := make([]string, len(cs))
	for i, c := range cs {
		names[i] = strings.ToLower(c.Plural)
	}
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

func dishesBy(dishes []Dish, name string) []Dish {
	var out []Dish
	for _, d := range dishes {
		if sameName(d.BroughtBy, name) {
			out = append(out, d)
		}
	}
	return out
}

type dishForm struct {
	Dish   Dish
	Errors map[string]string
}

func (f dishForm) HasTag(key string) bool {
	for _, t := range f.Dish.Tags {
		if t == key {
			return true
		}
	}
	return false
}

func parseDish(r *http.Request) dishForm {
	r.ParseForm()
	f := dishForm{
		Dish: Dish{
			Name:      strings.Join(strings.Fields(r.PostForm.Get("name")), " "),
			Category:  r.PostForm.Get("category"),
			NeedsOven: r.PostForm.Get("needs_oven") != "",
			OvenNotes: strings.TrimSpace(r.PostForm.Get("oven_notes")),
			Notes:     strings.TrimSpace(r.PostForm.Get("notes")),
		},
		Errors: map[string]string{},
	}
	for _, k := range r.PostForm["tags"] {
		if tagByKey(k) != nil && !f.HasTag(k) {
			f.Dish.Tags = append(f.Dish.Tags, k)
		}
	}
	if !f.Dish.NeedsOven {
		f.Dish.OvenNotes = ""
	}
	switch n := utf8.RuneCountInString(f.Dish.Name); {
	case n == 0:
		f.Errors["name"] = "What are you bringing?"
	case n > 80:
		f.Errors["name"] = "Keep the dish name under 80 characters."
	}
	if categoryByKey(f.Dish.Category) == nil {
		f.Errors["category"] = "Pick a course."
	}
	if utf8.RuneCountInString(f.Dish.OvenNotes) > 80 {
		f.Errors["oven_notes"] = "Keep oven details under 80 characters."
	}
	if utf8.RuneCountInString(f.Dish.Notes) > 280 {
		f.Errors["notes"] = "Keep notes under 280 characters."
	}
	return f
}

type tab struct {
	Label  string
	Count  int
	Href   string
	Active bool
}

type potluckPage struct {
	Menu     menu
	Sections []course
	Tabs     []tab
	Filter   string
	OvenOnly bool
	OvenHref string // toggles the oven-only filter
	Mine     int
	Form     dishForm
}

func potluckHref(course string, ovenOnly bool) string {
	v := url.Values{}
	if course != "" {
		v.Set("course", course)
	}
	if ovenOnly {
		v.Set("oven", "1")
	}
	if len(v) == 0 {
		return "/potluck#menu"
	}
	return "/potluck?" + v.Encode() + "#menu"
}

func (a *app) potluck(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	form := dishForm{}
	if c := categoryByKey(q.Get("add")); c != nil {
		form.Dish.Category = c.Key
	}
	a.renderPotluck(w, r, http.StatusOK, q.Get("course"), q.Get("oven") == "1", form)
}

func (a *app) renderPotluck(w http.ResponseWriter, r *http.Request, status int, filter string, ovenOnly bool, form dishForm) {
	dishes, err := a.store.ListDishes(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if categoryByKey(filter) == nil {
		filter = ""
	}
	m := buildMenu(dishes)
	p := potluckPage{
		Menu:     m,
		Filter:   filter,
		OvenOnly: ovenOnly,
		Mine:     len(dishesBy(dishes, userName(r))),
		OvenHref: potluckHref(filter, !ovenOnly),
		Form:     form,
		Tabs:     []tab{{Label: "All dishes", Count: m.Total, Href: potluckHref("", ovenOnly), Active: filter == ""}},
	}
	for _, c := range m.Courses {
		p.Tabs = append(p.Tabs, tab{Label: c.Plural, Count: len(c.Dishes), Href: potluckHref(c.Key, ovenOnly), Active: filter == c.Key})
		if filter != "" && c.Key != filter {
			continue
		}
		if ovenOnly {
			var hot []Dish
			for _, d := range c.Dishes {
				if d.NeedsOven {
					hot = append(hot, d)
				}
			}
			c.Dishes = hot
		}
		p.Sections = append(p.Sections, c)
	}
	a.render(w, r, status, "potluck", view{Title: "Potluck", Nav: "potluck", Page: p})
}

func (a *app) createDish(w http.ResponseWriter, r *http.Request) {
	f := parseDish(r)
	if len(f.Errors) > 0 {
		a.renderPotluck(w, r, http.StatusUnprocessableEntity, "", false, f)
		return
	}
	f.Dish.BroughtBy = userName(r)
	id, err := a.store.CreateDish(r.Context(), f.Dish)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	setFlash(w, r, f.Dish.Name+" is on the menu!")
	http.Redirect(w, r, "/potluck#dish-"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// ownDish loads the dish named in the URL and checks it belongs to the
// current user, writing an error response and returning false if not.
func (a *app) ownDish(w http.ResponseWriter, r *http.Request) (Dish, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		a.notFound(w, r)
		return Dish{}, false
	}
	d, err := a.store.GetDish(r.Context(), id)
	if errors.Is(err, errNotFound) {
		a.notFound(w, r)
		return d, false
	}
	if err != nil {
		a.serverError(w, r, err)
		return d, false
	}
	if !sameName(d.BroughtBy, userName(r)) {
		setFlash(w, r, "Only "+d.BroughtBy+" can change "+d.Name+".")
		http.Redirect(w, r, "/potluck", http.StatusSeeOther)
		return d, false
	}
	return d, true
}

type editDishPage struct {
	ID   int64
	Name string // as saved, while the form may hold a rejected edit
	Form dishForm
}

func (a *app) editDish(w http.ResponseWriter, r *http.Request) {
	d, ok := a.ownDish(w, r)
	if !ok {
		return
	}
	a.render(w, r, http.StatusOK, "dish_edit", view{Title: "Edit " + d.Name, Nav: "potluck",
		Page: editDishPage{ID: d.ID, Name: d.Name, Form: dishForm{Dish: d}}})
}

func (a *app) updateDish(w http.ResponseWriter, r *http.Request) {
	d, ok := a.ownDish(w, r)
	if !ok {
		return
	}
	f := parseDish(r)
	f.Dish.ID = d.ID
	if len(f.Errors) > 0 {
		a.render(w, r, http.StatusUnprocessableEntity, "dish_edit", view{Title: "Edit " + d.Name, Nav: "potluck",
			Page: editDishPage{ID: d.ID, Name: d.Name, Form: f}})
		return
	}
	if err := a.store.UpdateDish(r.Context(), f.Dish); err != nil {
		a.serverError(w, r, err)
		return
	}
	setFlash(w, r, "Saved your changes to "+f.Dish.Name+".")
	http.Redirect(w, r, "/potluck#dish-"+strconv.FormatInt(d.ID, 10), http.StatusSeeOther)
}

func (a *app) deleteDish(w http.ResponseWriter, r *http.Request) {
	d, ok := a.ownDish(w, r)
	if !ok {
		return
	}
	if err := a.store.DeleteDish(r.Context(), d.ID); err != nil {
		a.serverError(w, r, err)
		return
	}
	setFlash(w, r, d.Name+" is off the menu.")
	http.Redirect(w, r, "/potluck", http.StatusSeeOther)
}
