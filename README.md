# Brogiving 2026 🦃

A small, private coordination site for Thanksgiving: a potluck sign-up board
(with oven tracking), event details, and a forum, behind one shared password.

- **Potluck**: claim a dish, pick a course (appetizer, main, side, dessert,
  beverage), flag whether it needs the oven (plus temp and time), and add
  notes and dietary tags. The oven lineup lists everything that needs oven time.
- **Event details**: date, time, and location, configured by environment
  variables. All three default to TBD.
- **RSVPs**: say you're in (or can't make it) and add the people you're
  bringing, marking kids. The details page lists everyone coming with an
  adult/kid headcount.
- **Forum**: threads and replies.
- **No accounts**: friends enter the shared password and a name. The name lives
  in a cookie, and people can edit or delete only the dishes and posts under
  their current name.

It's a single Go binary with SQLite, server-rendered HTML, one stylesheet,
self-hosted fonts and icons, and a few lines of optional JavaScript. Pages
render in well under a millisecond.

## Deploy

```sh
cp .env.example .env   # set SITE_PASSWORD, and event details when known
docker compose up -d --build
```

The site listens on `PORT` (default 8080). Put it behind your usual TLS
reverse proxy (Caddy, nginx, Traefik). Cookies are marked `Secure` when
requests arrive over HTTPS or with `X-Forwarded-Proto: https`.

Data lives in SQLite at `/data/brogiving.db` on the `brogiving-data` volume.
To back it up:

```sh
docker compose cp brogiving:/data/brogiving.db ./backup.db
```

To change event details later, edit `.env` and run `docker compose up -d`.

## Configuration

| Variable         | Default              | Notes                                         |
| ---------------- | -------------------- | --------------------------------------------- |
| `SITE_PASSWORD`  | (required)           | Shared password. Changing it signs everyone out. |
| `EVENT_NAME`     | `Brogiving 2026`     |                                               |
| `EVENT_DATE`     | `TBD`                | Free text, e.g. `Thursday, November 26`       |
| `EVENT_TIME`     | `TBD`                | Free text, e.g. `3:00 PM`                     |
| `EVENT_LOCATION` | `TBD`                | Free text, e.g. `Mike's place`                |
| `EVENT_ADDRESS`  | (empty)              | When set, adds an "Open in Maps" button       |
| `TZ`             | `UTC`                | Timezone for hover timestamps                 |
| `ADDR`           | `:8080`              | Listen address inside the container           |
| `DB_PATH`        | `/data/brogiving.db` | SQLite file                                   |

## Develop

```sh
SITE_PASSWORD=dev go run .   # http://localhost:8080
go test ./...
```

Templates and static files are embedded into the binary, so restart after
editing them. The design comes from the "Friendsgiving Feast Coordinator"
Stitch project, with its tokens hand-translated into `web/static/app.css`.
