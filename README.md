# Brogiving 2026 🦃

A small, private coordination site for Thanksgiving: a potluck sign-up board
(with oven tracking), event details, and a forum, behind one shared password.

- **Potluck**: claim a dish, pick a course (appetizer, main, side, dessert,
  beverage), flag whether it needs the oven (plus temp and time), and add
  notes and dietary tags. The oven lineup lists everything that needs oven time.
- **Event details**: date, time, and location, configured by environment
  variables. All three default to TBD.
- **Forum**: threads and replies.
- **No accounts**: friends enter the shared password and a name. The name lives
  in a cookie, and people can edit or delete only the dishes and posts under
  their current name.
- **Bring your Claude**: an MCP server at `/mcp` lets Claude (or any MCP
  client) read the plan, claim dishes, and post in the forum. Setup steps are
  at `/mcp/docs`, linked from the footer.

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

## Post log

Names are self-declared, so the site keeps a paper trail. Every POST is
recorded with its endpoint, response status, the name the sender was using,
their IP address, and the fields they sent, with passwords replaced by
`[redacted]`. Anyone signed in can read it at `/logs`, which nothing links to,
and filter it by name, endpoint, IP address, or content.

MCP calls that change something are logged the same way, under the name the
client signed in with: the endpoint is `/mcp/<tool>`, the fields are the
tool's arguments, and the status is 422 when the tool refused. Failed MCP
sign-ins are logged as `/mcp` with the name they tried. Read-only tools, like
GET pages, aren't logged.

The IP address comes from `Fly-Client-IP` on Fly.io, from the last
`X-Forwarded-For` hop when a reverse proxy on a private or loopback address
sends one, and from the connection otherwise.

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

## MCP

`/mcp` speaks the Model Context Protocol's Streamable HTTP transport,
statelessly: one JSON-RPC message per POST, one JSON reply, no sessions or
streams. It skips the cookie gate and takes the same `SITE_PASSWORD` instead,
either as HTTP Basic auth (any name as the username) or as query parameters
for apps that only take a URL:

```sh
claude mcp add --transport http brogiving 'https://your-site/mcp?name=Chris&password=...'
```

The name is who Claude posts as, with the same ownership rules as the site.
Tools cover the event details, the potluck (list, add, update, remove
dishes), the forum (list, read, start, reply, delete), and who's involved.

## Develop

```sh
SITE_PASSWORD=dev go run .   # http://localhost:8080
go test ./...
```

Templates and static files are embedded into the binary, so restart after
editing them. The design comes from the "Friendsgiving Feast Coordinator"
Stitch project, with its tokens hand-translated into `web/static/app.css`.
