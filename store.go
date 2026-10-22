package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

type Dish struct {
	ID        int64
	Name      string
	Category  string
	NeedsOven bool
	OvenNotes string
	Notes     string
	Tags      []string
	BroughtBy string
	CreatedAt time.Time
}

type Thread struct {
	ID           int64
	Title        string
	Body         string
	Author       string
	CreatedAt    time.Time
	LastActivity time.Time
	ReplyCount   int
}

type Reply struct {
	ID        int64
	ThreadID  int64
	Body      string
	Author    string
	CreatedAt time.Time
}

const dishCols = `id, name, category, needs_oven, oven_notes, notes, tags, brought_by, created_at`

func scanDish(row interface{ Scan(...any) error }) (Dish, error) {
	var d Dish
	var tags string
	var created int64
	err := row.Scan(&d.ID, &d.Name, &d.Category, &d.NeedsOven, &d.OvenNotes, &d.Notes, &tags, &d.BroughtBy, &created)
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

func (s *Store) CreateReply(ctx context.Context, r Reply) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO replies (thread_id, body, author, created_at) VALUES (?, ?, ?, ?)`,
		r.ThreadID, r.Body, r.Author, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE threads SET reply_count = reply_count + 1, last_activity_at = ? WHERE id = ?`, now, r.ThreadID); err != nil {
		return err
	}
	return tx.Commit()
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
	ID     int64
	Name   string
	Going  bool
	Guests []Guest
}

// Size counts the person who RSVPed and their guests.
func (r RSVP) Size() int { return 1 + len(r.Guests) }

type Guest struct {
	ID   int64
	Name string
	Kid  bool
	By   string // who added them
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
	Name   string
	Dishes int
	Posts  int
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
			(SELECT COUNT(*) FROM threads),
			(SELECT COUNT(*) FROM replies),
			(SELECT MAX(last_activity_at) FROM threads)`).
		Scan(&c.Dishes, &c.Oven, &c.Threads, &c.Replies, &last)
	if last.Valid {
		c.LastPost, c.HasPosted = time.Unix(last.Int64, 0), true
	}
	return c, err
}
