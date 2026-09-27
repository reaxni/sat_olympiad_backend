package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type Server struct {
	db                *sql.DB
	env               string
	origins           map[string]bool
	secret            []byte
	examID, examTitle string
	openAt, closeAt   time.Time
	secureCookie      bool
	cookieSameSite    http.SameSite
}

func env(name, fallback string) string {
	if s := strings.TrimSpace(os.Getenv(name)); s != "" {
		return s
	}
	return fallback
}
func parseTime(name string) (time.Time, error) {
	s := os.Getenv(name)
	if s == "" {
		return time.Time{}, fmt.Errorf("%s must be set", name)
	}
	t, e := time.Parse(time.RFC3339, s)
	return t.UTC(), e
}

func Run() error {
	appEnv := env("APP_ENV", "production")
	if appEnv != "development" && appEnv != "production" {
		return errors.New("APP_ENV must be development or production")
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL must be set")
	}
	secret := os.Getenv("AUTH_SECRET")
	if len(secret) < 32 {
		return errors.New("AUTH_SECRET must contain at least 32 characters")
	}
	openAt, e := parseTime("EXAM_OPEN_AT")
	if e != nil {
		return e
	}
	closeAt, e := parseTime("EXAM_ENTRY_CLOSE_AT")
	if e != nil {
		return e
	}
	if !closeAt.After(openAt) {
		return errors.New("entry close must be after opening")
	}
	origins := map[string]bool{}
	for _, o := range strings.Split(os.Getenv("ALLOWED_ORIGINS"), ",") {
		o = strings.TrimSpace(o)
		if o != "" {
			origins[o] = true
		}
	}
	if len(origins) == 0 {
		return errors.New("ALLOWED_ORIGINS must contain the frontend origin")
	}
	if appEnv == "production" && os.Getenv("DEV_EMAIL_LOG") == "true" {
		return errors.New("DEV_EMAIL_LOG is forbidden in production")
	}
	cookieMode := env("COOKIE_SAMESITE", "lax")
	if cookieMode != "lax" && cookieMode != "none" {
		return errors.New("COOKIE_SAMESITE must be lax or none")
	}
	if cookieMode == "none" && appEnv != "production" {
		return errors.New("COOKIE_SAMESITE=none requires production HTTPS")
	}
	db, e := sql.Open("pgx", dsn)
	if e != nil {
		return e
	}
	defer db.Close()
	db.SetMaxOpenConns(12)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(30 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if e = db.PingContext(ctx); e != nil {
		return fmt.Errorf("database: %w", e)
	}
	migration, e := os.ReadFile("migrations/001_init.sql")
	if e != nil {
		return e
	}
	if _, e = db.ExecContext(ctx, string(migration)); e != nil {
		return fmt.Errorf("migration: %w", e)
	}
	s := &Server{db: db, env: appEnv, origins: origins, secret: []byte(secret), examID: env("EXAM_ID", "1609-olympiad"), examTitle: env("EXAM_TITLE", "1609 SAT Olympiad"), openAt: openAt, closeAt: closeAt, secureCookie: appEnv == "production", cookieSameSite: http.SameSiteLaxMode}
	if cookieMode == "none" {
		s.cookieSameSite = http.SameSiteNoneMode
	}
	if _, e = db.ExecContext(ctx, `INSERT INTO exams(id,title,opens_at,entry_closes_at) VALUES($1,$2,$3,$4) ON CONFLICT(id) DO UPDATE SET title=EXCLUDED.title,opens_at=EXCLUDED.opens_at,entry_closes_at=EXCLUDED.entry_closes_at`, s.examID, s.examTitle, s.openAt, s.closeAt); e != nil {
		return e
	}
	mux := http.NewServeMux()
	s.routes(mux)
	go s.reconcileLoop()
	port := env("PORT", "8080")
	if _, e = strconv.Atoi(port); e != nil {
		return errors.New("PORT must be numeric")
	}
	log.Printf("SAT backend listening on port %s (%s)", port, appEnv)
	h := &http.Server{Addr: ":" + port, Handler: s.middleware(mux), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second}
	return h.ListenAndServe()
}

type apiError struct {
	Status        int
	Code, Message string
}

func (e apiError) Error() string                  { return e.Message }
func fail(status int, code, message string) error { return apiError{status, code, message} }
func handle(fn func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if e := fn(w, r); e != nil {
			var a apiError
			if errors.As(e, &a) {
				writeJSON(w, a.Status, map[string]any{"error": map[string]string{"code": a.Code, "message": a.Message}})
				return
			}
			log.Printf("%s %s: %v", r.Method, r.URL.Path, e)
			writeJSON(w, 503, map[string]any{"error": map[string]string{"code": "SERVICE_UNAVAILABLE", "message": "The exam service is temporarily unavailable."}})
		}
	}
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func ok(w http.ResponseWriter, v any) {
	writeJSON(w, 200, map[string]any{"data": v, "serverTime": time.Now().UTC().Format(time.RFC3339Nano)})
}
func decode(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return fail(400, "VALIDATION_ERROR", "Invalid request body.")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return fail(400, "VALIDATION_ERROR", "Invalid request body.")
	}
	return nil
}
func newID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func (s *Server) codeHash(id, code string) string {
	h := hmac.New(sha256.New, s.secret)
	_, _ = h.Write([]byte(id + ":" + code))
	return hex.EncodeToString(h.Sum(nil))
}
func errDB(e error) error {
	if errors.Is(e, sql.ErrNoRows) {
		return fail(404, "NOT_FOUND", "The requested item was not found.")
	}
	return e
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			if !s.origins[origin] {
				writeJSON(w, 403, map[string]any{"error": map[string]string{"code": "FORBIDDEN", "message": "Origin is not allowed."}})
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Olympiad-Request")
			w.WriteHeader(204)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if origin == "" && s.env == "production" {
				writeJSON(w, 403, map[string]any{"error": map[string]string{"code": "FORBIDDEN", "message": "Origin is required."}})
				return
			}
			if r.Header.Get("X-Olympiad-Request") != "1" {
				writeJSON(w, 403, map[string]any{"error": map[string]string{"code": "FORBIDDEN", "message": "Request header missing."}})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) routes(m *http.ServeMux) {
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if s.db.PingContext(ctx) != nil {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	})
	m.HandleFunc("GET /service", handle(func(w http.ResponseWriter, r *http.Request) error {
		// This is the real API even when it runs locally; the UI's development label means mock data.
		ok(w, map[string]any{"status": "available", "environment": "production"})
		return nil
	}))
	m.HandleFunc("POST /auth/email/request", handle(s.requestCode))
	m.HandleFunc("POST /auth/email/resend", handle(s.resendCode))
	m.HandleFunc("POST /auth/email/verify", handle(s.verifyCode))
	m.HandleFunc("GET /auth/me", handle(s.me))
	m.HandleFunc("POST /auth/sign-out", handle(s.signOut))
	m.HandleFunc("GET /exam", handle(s.schedule))
	m.HandleFunc("GET /exams/{exam}/eligibility", handle(s.eligibility))
	m.HandleFunc("GET /exams/{exam}/attempt", handle(s.activeAttempt))
	m.HandleFunc("POST /exams/{exam}/attempts", handle(s.createAttempt))
	m.HandleFunc("GET /attempts/{attempt}", handle(s.getAttempt))
	m.HandleFunc("POST /attempts/{attempt}/sections/{section}/start", handle(s.startSection))
	m.HandleFunc("GET /attempts/{attempt}/sections/{section}", handle(s.getSection))
	m.HandleFunc("PUT /attempts/{attempt}/answers/{question}", handle(s.saveAnswer))
	m.HandleFunc("POST /attempts/{attempt}/sections/{section}/submit", handle(s.submitSection))
	m.HandleFunc("POST /attempts/{attempt}/violations", handle(s.violation))
	m.HandleFunc("GET /attempts/{attempt}/result", handle(s.result))
	m.HandleFunc("GET /exams/{exam}/releases", handle(s.releases))
	m.HandleFunc("GET /attempts/{attempt}/review", handle(s.review))
	m.HandleFunc("GET /exams/{exam}/leaderboard", handle(s.leaderboard))
}
func (s *Server) requireExam(r *http.Request) error {
	if r.PathValue("exam") != s.examID {
		return fail(404, "NOT_FOUND", "Exam not found.")
	}
	return nil
}
func (s *Server) student(r *http.Request) (Student, error) {
	var u Student
	c, e := r.Cookie("sat_session")
	if e != nil {
		return u, fail(401, "UNAUTHENTICATED", "Please sign in again.")
	}
	e = s.db.QueryRowContext(r.Context(), `SELECT u.id,u.name,u.grade,u.email FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.expires_at>now()`, hash(c.Value)).Scan(&u.ID, &u.Name, &u.Grade, &u.Email)
	if errors.Is(e, sql.ErrNoRows) {
		return u, fail(401, "UNAUTHENTICATED", "Please sign in again.")
	}
	return u, e
}
func (s *Server) maybeStudent(r *http.Request) *Student {
	u, e := s.student(r)
	if e != nil {
		return nil
	}
	return &u
}

type Student struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Grade int    `json:"grade"`
	Email string `json:"email"`
}

func (s *Server) clientIP(r *http.Request) string {
	host, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil {
		return r.RemoteAddr
	}
	return host
}
