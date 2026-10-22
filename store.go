package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var errNotFound = errors.New("not found")

const schema = `
CREATE TABLE IF NOT EXISTS dishes (
	id          INTEGER PRIMARY KEY,
	name        TEXT    NOT NULL,
	category    TEXT    NOT NULL,
	needs_oven  INTEGER NOT NULL DEFAULT 0,
	oven_notes  TEXT    NOT NULL DEFAULT '',
	notes       TEXT    NOT NULL DEFAULT '',
	tags        TEXT    NOT NULL DEFAULT '',
	brought_by  TEXT    NOT NULL,
	created_at  INTEGER NOT NULL,
	updated_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS dish_comments (
	id         INTEGER PRIMARY KEY,
	dish_id    INTEGER NOT NULL REFERENCES dishes (id) ON DELETE CASCADE,
	body       TEXT    NOT NULL,
	author     TEXT    NOT NULL,
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS dish_comments_by_dish ON dish_comments (dish_id, created_at);

CREATE TABLE IF NOT EXISTS threads (
	id               INTEGER PRIMARY KEY,
	title            TEXT    NOT NULL,
	body             TEXT    NOT NULL,
	author           TEXT    NOT NULL,
	created_at       INTEGER NOT NULL,
	last_activity_at INTEGER NOT NULL,
	reply_count      INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS threads_by_activity ON threads (last_activity_at DESC);

CREATE TABLE IF NOT EXISTS replies (
	id         INTEGER PRIMARY KEY,
	thread_id  INTEGER NOT NULL REFERENCES threads (id) ON DELETE CASCADE,
	body       TEXT    NOT NULL,
	author     TEXT    NOT NULL,
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS replies_by_thread ON replies (thread_id, created_at);

CREATE TABLE IF NOT EXISTS rsvps (
	id         INTEGER PRIMARY KEY,
	name       TEXT    NOT NULL UNIQUE COLLATE NOCASE,
	going      INTEGER NOT NULL,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS guests (
	id         INTEGER PRIMARY KEY,
	rsvp_id    INTEGER NOT NULL REFERENCES rsvps (id) ON DELETE CASCADE,
	name       TEXT    NOT NULL,
	kid        INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS guests_by_rsvp ON guests (rsvp_id, created_at);

CREATE TABLE IF NOT EXISTS post_log (
	id         INTEGER PRIMARY KEY,
	created_at INTEGER NOT NULL,
	path       TEXT    NOT NULL,
	status     INTEGER NOT NULL,
	user_name  TEXT    NOT NULL,
	ip         TEXT    NOT NULL,
	form       TEXT    NOT NULL
);
`

type Store struct {
	db *sql.DB
}

func openStore(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// The JSON names are what MCP tools take and return.
type Dish struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Category  string    `json:"course"`
	NeedsOven bool      `json:"needs_oven"`
	OvenNotes string    `json:"oven_notes,omitempty"`
	Notes     string    `json:"notes,omitempty"`
	Tags      []string  `json:"dietary_tags,omitempty"`
	BroughtBy string    `json:"brought_by"`
	CreatedAt time.Time `json:"added_at"`

	Comments      int    `json:"comments"`
	LastCommentBy string `json:"last_comment_by,omitempty"`
}

// AwaitingReply reports whether someone else has the last word in the
// dish's comments, so the person bringing it may want to answer.
func (d Dish) AwaitingReply() bool {
	return d.LastCommentBy != "" && !sameName(d.LastCommentBy, d.BroughtBy)
}

// Comment is one message in a dish's discussion. Each dish has a single
// running conversation, oldest first, with no nesting.
type Comment struct {
	ID        int64     `json:"id"`
	DishID    int64     `json:"dish_id"`
	Body      string    `json:"body"`
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"created_at"`
}

type Thread struct {
	ID           int64     `json:"id"`
	Title        string    `json:"title"`
	Body         string    `json:"body"`
	Author       string    `json:"author"`
	CreatedAt    time.Time `json:"created_at"`
	LastActivity time.Time `json:"last_activity_at"`
	ReplyCount   int       `json:"reply_count"`
}

type Reply struct {
	ID        int64     `json:"id"`
	ThreadID  int64     `json:"thread_id"`
	Body      string    `json:"body"`
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"created_at"`
}

const dishCols = `id, name, category, needs_oven, oven_notes, notes, tags, brought_by, created_at,
	(SELECT COUNT(*) FROM dish_comments WHERE dish_id = dishes.id),
	COALESCE((SELECT author FROM dish_comments WHERE dish_id = dishes.id ORDER BY created_at DESC, id DESC LIMIT 1), '')`

func scanDish(row interface{ Scan(...any) error }) (Dish, error) {
	var d Dish
	var tags string
	var created int64
	err := row.Scan(&d.ID, &d.Name, &d.Category, &d.NeedsOven, &d.OvenNotes, &d.Notes, &tags, &d.BroughtBy, &created, &d.Comments, &d.LastCommentBy)
	if tags != "" {
		d.Tags = strings.Split(tags, ",")
	}
	d.CreatedAt = time.Unix(created, 0)
	return d, err
}

func (s *Store) ListDishes(ctx context.Context) ([]Dish, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+dishCols+` FROM dishes ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Dish
	for rows.Next() {
		d, err := scanDish(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) GetDish(ctx context.Context, id int64) (Dish, error) {
	d, err := scanDish(s.db.QueryRowContext(ctx, `SELECT `+dishCols+` FROM dishes WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return d, errNotFound
	}
	return d, err
}

func (s *Store) CreateDish(ctx context.Context, d Dish) (int64, error) {
	now := time.Now().Unix()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO dishes (name, category, needs_oven, oven_notes, notes, tags, brought_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.Name, d.Category, d.NeedsOven, d.OvenNotes, d.Notes, strings.Join(d.Tags, ","), d.BroughtBy, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateDish(ctx context.Context, d Dish) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE dishes SET name = ?, category = ?, needs_oven = ?, oven_notes = ?, notes = ?, tags = ?, updated_at = ?
		 WHERE id = ?`,
		d.Name, d.Category, d.NeedsOven, d.OvenNotes, d.Notes, strings.Join(d.Tags, ","), time.Now().Unix(), d.ID)
	return err
}

func (s *Store) DeleteDish(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM dishes WHERE id = ?`, id)
	return err
}

const commentCols = `id, dish_id, body, author, created_at`

func scanComment(row interface{ Scan(...any) error }) (Comment, error) {
	var c Comment
	var created int64
	err := row.Scan(&c.ID, &c.DishID, &c.Body, &c.Author, &created)
	c.CreatedAt = time.Unix(created, 0)
	return c, err
}

func (s *Store) ListComments(ctx context.Context, dishID int64) ([]Comment, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+commentCols+` FROM dish_comments WHERE dish_id = ? ORDER BY created_at, id`, dishID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Comment
	for rows.Next() {
		c, err := scanComment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetComment(ctx context.Context, id int64) (Comment, error) {
	c, err := scanComment(s.db.QueryRowContext(ctx, `SELECT `+commentCols+` FROM dish_comments WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, errNotFound
	}
	return c, err
}

func (s *Store) CreateComment(ctx context.Context, c Comment) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO dish_comments (dish_id, body, author, created_at) VALUES (?, ?, ?, ?)`,
		c.DishID, c.Body, c.Author, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) DeleteComment(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM dish_comments WHERE id = ?`, id)
	return err
}

const threadCols = `id, title, body, author, created_at, last_activity_at, reply_count`

func scanThread(row interface{ Scan(...any) error }) (Thread, error) {
	var t Thread
	var created, active int64
	err := row.Scan(&t.ID, &t.Title, &t.Body, &t.Author, &created, &active, &t.ReplyCount)
	t.CreatedAt = time.Unix(created, 0)
	t.LastActivity = time.Unix(active, 0)
	return t, err
}

// ListThreads returns threads with the most recently active first. A limit of
// zero or less returns all of them.
func (s *Store) ListThreads(ctx context.Context, limit int) ([]Thread, error) {
	if limit <= 0 {
		limit = -1
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+threadCols+` FROM threads ORDER BY last_activity_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Thread
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) GetThread(ctx context.Context, id int64) (Thread, error) {
	t, err := scanThread(s.db.QueryRowContext(ctx, `SELECT `+threadCols+` FROM threads WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return t, errNotFound
	}
	return t, err
}

func (s *Store) CreateThread(ctx context.Context, t Thread) (int64, error) {
	now := time.Now().Unix()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO threads (title, body, author, created_at, last_activity_at) VALUES (?, ?, ?, ?, ?)`,
		t.Title, t.Body, t.Author, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) DeleteThread(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM threads WHERE id = ?`, id)
	return err
}

func (s *Store) ListReplies(ctx context.Context, threadID int64) ([]Reply, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, thread_id, body, author, created_at FROM replies WHERE thread_id = ? ORDER BY created_at, id`, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Reply
	for rows.Next() {
		var r Reply
		var created int64
		if err := rows.Scan(&r.ID, &r.ThreadID, &r.Body, &r.Author, &created); err != nil {
			return nil, err
		}
		r.CreatedAt = time.Unix(created, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetReply(ctx context.Context, id int64) (Reply, error) {
	var r Reply
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT id, thread_id, body, author, created_at FROM replies WHERE id = ?`, id).
		Scan(&r.ID, &r.ThreadID, &r.Body, &r.Author, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return r, errNotFound
	}
	r.CreatedAt = time.Unix(created, 0)
	return r, err
}

func (s *Store) CreateReply(ctx context.Context, r Reply) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO replies (thread_id, body, author, created_at) VALUES (?, ?, ?, ?)`,
		r.ThreadID, r.Body, r.Author, now)
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE threads SET reply_count = reply_count + 1, last_activity_at = ? WHERE id = ?`, now, r.ThreadID); err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func (s *Store) DeleteReply(ctx context.Context, r Reply) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM replies WHERE id = ?`, r.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE threads SET reply_count = MAX(reply_count - 1, 0) WHERE id = ?`, r.ThreadID); err != nil {
		return err
	}
	return tx.Commit()
}

// RSVP is one person's answer, plus anyone they're bringing. Like dishes and
// posts, it belongs to whoever holds the name.
type RSVP struct {
	ID     int64   `json:"-"`
	Name   string  `json:"name"`
	Going  bool    `json:"going"`
	Guests []Guest `json:"guests,omitempty"`
}

// Size counts the person who RSVPed and their guests.
func (r RSVP) Size() int { return 1 + len(r.Guests) }

// Kids and Adults count the whole party; whoever RSVPed is an adult.
func (r RSVP) Kids() int   { return len(r.KidGuests()) }
func (r RSVP) Adults() int { return r.Size() - r.Kids() }

func (r RSVP) KidGuests() []Guest   { return r.guests(true) }
func (r RSVP) AdultGuests() []Guest { return r.guests(false) }

func (r RSVP) guests(kids bool) []Guest {
	var out []Guest
	for _, g := range r.Guests {
		if g.Kid == kids {
			out = append(out, g)
		}
	}
	return out
}

type Guest struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Kid  bool   `json:"kid"`
	By   string `json:"-"` // who added them
}

func (s *Store) ListRSVPs(ctx context.Context) ([]RSVP, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, going FROM rsvps ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RSVP
	index := map[int64]int{}
	for rows.Next() {
		var r RSVP
		if err := rows.Scan(&r.ID, &r.Name, &r.Going); err != nil {
			return nil, err
		}
		index[r.ID] = len(out)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.db.QueryContext(ctx, `SELECT id, rsvp_id, name, kid FROM guests ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var g Guest
		var rsvpID int64
		if err := rows.Scan(&g.ID, &rsvpID, &g.Name, &g.Kid); err != nil {
			return nil, err
		}
		if i, ok := index[rsvpID]; ok {
			g.By = out[i].Name
			out[i].Guests = append(out[i].Guests, g)
		}
	}
	return out, rows.Err()
}

const upsertRSVP = `
	INSERT INTO rsvps (name, going, created_at, updated_at) VALUES (?, ?, ?, ?)
	ON CONFLICT (name) DO UPDATE SET name = excluded.name, going = excluded.going, updated_at = excluded.updated_at
	RETURNING id`

func (s *Store) SetRSVP(ctx context.Context, name string, going bool) error {
	now := time.Now().Unix()
	var id int64
	return s.db.QueryRowContext(ctx, upsertRSVP, name, going, now, now).Scan(&id)
}

// AddGuest adds someone to name's party. Bringing a guest means you're
// coming too, so it also marks name as going.
func (s *Store) AddGuest(ctx context.Context, name string, g Guest) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	var rsvpID int64
	if err := tx.QueryRowContext(ctx, upsertRSVP, name, true, now, now).Scan(&rsvpID); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO guests (rsvp_id, name, kid, created_at) VALUES (?, ?, ?, ?)`, rsvpID, g.Name, g.Kid, now)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func (s *Store) GetGuest(ctx context.Context, id int64) (Guest, error) {
	var g Guest
	err := s.db.QueryRowContext(ctx,
		`SELECT g.id, g.name, g.kid, r.name FROM guests g JOIN rsvps r ON r.id = g.rsvp_id WHERE g.id = ?`, id).
		Scan(&g.ID, &g.Name, &g.Kid, &g.By)
	if errors.Is(err, sql.ErrNoRows) {
		return g, errNotFound
	}
	return g, err
}

func (s *Store) DeleteGuest(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM guests WHERE id = ?`, id)
	return err
}

// Person is anyone who has signed up for a dish or posted in the forum.
type Person struct {
	Name   string `json:"name"`
	Dishes int    `json:"dishes"`
	Posts  int    `json:"posts"`
}

func (s *Store) People(ctx context.Context) ([]Person, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT name, SUM(dishes), SUM(posts) FROM (
			SELECT brought_by AS name, 1 AS dishes, 0 AS posts FROM dishes
			UNION ALL SELECT author, 0, 1 FROM threads
			UNION ALL SELECT author, 0, 1 FROM replies
		)
		GROUP BY name COLLATE NOCASE
		ORDER BY SUM(dishes) + SUM(posts) DESC, name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Person
	for rows.Next() {
		var p Person
		if err := rows.Scan(&p.Name, &p.Dishes, &p.Posts); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

type Counts struct {
	Dishes    int
	Oven      int
	Coming    int // everyone RSVPed yes, guests included
	Threads   int
	Replies   int
	LastPost  time.Time
	HasPosted bool
}

func (s *Store) Counts(ctx context.Context) (Counts, error) {
	var c Counts
	var last sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM dishes),
			(SELECT COUNT(*) FROM dishes WHERE needs_oven),
			(SELECT COUNT(*) FROM rsvps WHERE going) +
				(SELECT COUNT(*) FROM guests JOIN rsvps ON rsvps.id = guests.rsvp_id WHERE going),
			(SELECT COUNT(*) FROM threads),
			(SELECT COUNT(*) FROM replies),
			(SELECT MAX(last_activity_at) FROM threads)`).
		Scan(&c.Dishes, &c.Oven, &c.Coming, &c.Threads, &c.Replies, &last)
	if last.Valid {
		c.LastPost, c.HasPosted = time.Unix(last.Int64, 0), true
	}
	return c, err
}

// PostLog is one POST request as the audit log saw it. Form holds the
// submitted fields, with any password redacted.
type PostLog struct {
	ID     int64
	At     time.Time
	Path   string
	Status int
	User   string
	IP     string
	Form   url.Values
}

func (s *Store) LogPost(ctx context.Context, p PostLog) error {
	form, err := json.Marshal(p.Form)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO post_log (created_at, path, status, user_name, ip, form) VALUES (?, ?, ?, ?, ?, ?)`,
		time.Now().Unix(), p.Path, p.Status, p.User, p.IP, string(form))
	return err
}

// LogFilter narrows the post log. Each non-empty field must appear, ignoring
// case, in its column; Text searches the submitted values, not field names.
type LogFilter struct {
	User string
	Path string
	IP   string
	Text string
}

func (f LogFilter) Active() bool { return f != (LogFilter{}) }

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// PostLogs returns up to limit entries matching f, newest first, and how
// many match in all.
func (s *Store) PostLogs(ctx context.Context, f LogFilter, limit int) ([]PostLog, int, error) {
	var where []string
	var args []any
	contains := func(cond, v string) {
		if v != "" {
			where = append(where, cond)
			args = append(args, "%"+likeEscaper.Replace(v)+"%")
		}
	}
	contains(`user_name LIKE ? ESCAPE '\'`, f.User)
	contains(`path LIKE ? ESCAPE '\'`, f.Path)
	contains(`ip LIKE ? ESCAPE '\'`, f.IP)
	contains(`EXISTS (SELECT 1 FROM json_tree(form) WHERE type = 'text' AND value LIKE ? ESCAPE '\')`, f.Text)
	cond := ""
	if len(where) > 0 {
		cond = " WHERE " + strings.Join(where, " AND ")
	}

	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM post_log`+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, created_at, path, status, user_name, ip, form FROM post_log`+cond+` ORDER BY id DESC LIMIT ?`,
		append(args, limit)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []PostLog
	for rows.Next() {
		var p PostLog
		var created int64
		var form string
		if err := rows.Scan(&p.ID, &created, &p.Path, &p.Status, &p.User, &p.IP, &form); err != nil {
			return nil, 0, err
		}
		p.At = time.Unix(created, 0)
		if err := json.Unmarshal([]byte(form), &p.Form); err != nil {
			return nil, 0, err
		}
		out = append(out, p)
	}
	return out, total, rows.Err()
}

// PostLogSuggestions lists the names, paths, and IPs seen in the post log,
// most frequent first, to offer as filter values.
func (s *Store) PostLogSuggestions(ctx context.Context) (users, paths, ips []string, err error) {
	distinct := func(col string) ([]string, error) {
		rows, err := s.db.QueryContext(ctx,
			`SELECT `+col+` FROM post_log WHERE `+col+` != '' GROUP BY `+col+` ORDER BY COUNT(*) DESC, `+col)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, rows.Err()
	}
	if users, err = distinct("user_name"); err != nil {
		return
	}
	if paths, err = distinct("path"); err != nil {
		return
	}
	ips, err = distinct("ip")
	return
}
