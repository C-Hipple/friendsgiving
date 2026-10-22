// Command brogiving serves the Brogiving 2026 coordination site: a potluck
// sign-up sheet, event details, and a small forum, all behind a shared password.
package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // lets TZ work in the distroless image
)

//go:embed web
var webFS embed.FS

type config struct {
	Addr     string
	DBPath   string
	Password string
	Event    eventInfo
}

type eventInfo struct {
	Name     string `json:"name"`
	Date     string `json:"date"`
	Time     string `json:"time"`
	Location string `json:"location"`
	Address  string `json:"address,omitempty"`
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func loadConfig() (config, error) {
	cfg := config{
		Addr:     env("ADDR", ":8080"),
		DBPath:   env("DB_PATH", "brogiving.db"),
		Password: os.Getenv("SITE_PASSWORD"),
		Event: eventInfo{
			Name:     env("EVENT_NAME", "Brogiving 2026"),
			Date:     env("EVENT_DATE", "TBD"),
			Time:     env("EVENT_TIME", "TBD"),
			Location: env("EVENT_LOCATION", "TBD"),
			Address:  env("EVENT_ADDRESS", ""),
		},
	}
	if cfg.Password == "" {
		return cfg, errors.New("SITE_PASSWORD must be set")
	}
	return cfg, nil
}

func main() {
	healthcheck := flag.Bool("healthcheck", false, "probe the running server's /healthz and exit")
	flag.Parse()

	if *healthcheck {
		os.Exit(probe(env("ADDR", ":8080")))
	}

	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	store, err := openStore(cfg.DBPath)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer store.Close()

	app, err := newApp(cfg, store)
	if err != nil {
		log.Fatal(err)
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           app.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()

	log.Printf("%s listening on %s", cfg.Event.Name, cfg.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

// probe is the container healthcheck; the distroless image has no curl.
func probe(addr string) int {
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + addr + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.Handle("GET /static/", a.static)
	mux.HandleFunc("GET /favicon.ico", a.static.favicon)

	mux.HandleFunc("GET /login", a.loginPage)
	mux.HandleFunc("POST /login", a.login)
	mux.HandleFunc("GET /name", a.namePage)
	mux.HandleFunc("POST /name", a.setName)

	mux.HandleFunc("GET /{$}", a.home)
	mux.HandleFunc("GET /details", a.details)

	mux.HandleFunc("GET /potluck", a.potluck)
	mux.HandleFunc("POST /potluck", a.createDish)
	mux.HandleFunc("GET /potluck/{id}/edit", a.editDish)
	mux.HandleFunc("POST /potluck/{id}", a.updateDish)
	mux.HandleFunc("POST /potluck/{id}/delete", a.deleteDish)

	mux.HandleFunc("GET /forum", a.forum)
	mux.HandleFunc("POST /forum", a.createThread)
	mux.HandleFunc("GET /forum/{id}", a.thread)
	mux.HandleFunc("POST /forum/{id}/delete", a.deleteThread)
	mux.HandleFunc("POST /forum/{id}/replies", a.createReply)
	mux.HandleFunc("POST /forum/{id}/replies/{rid}/delete", a.deleteReply)

	// Any method: /mcp skips the cookie gate, so a stray DELETE mustn't fall
	// through to the not-found page and its event details.
	mux.HandleFunc("/mcp", a.mcp)
	mux.HandleFunc("GET /mcp/docs", a.mcpDocs)

	mux.HandleFunc("/", a.notFound)

	return logRequests(secureHeaders(a.requireAuth(mux)))
}

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; form-action 'self'; base-uri 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Robots-Tag", "noindex, nofollow")
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		}
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Microsecond))
	})
}
