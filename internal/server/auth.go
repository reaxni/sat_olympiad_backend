package server

import (
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/smtp"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var emailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

type challenge struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	ExpiresAt time.Time `json:"expiresAt"`
	ResendAt  time.Time `json:"resendAt"`
}
type codeRequest struct {
	Purpose string `json:"purpose"`
	Name    string `json:"name"`
	Grade   int    `json:"grade"`
	Email   string `json:"email"`
}

func normalizeEmail(v string) string { return strings.ToLower(strings.TrimSpace(v)) }
func randomCode() string {
	b := make([]byte, 4)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	n := (uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])) % 1000000
	return fmt.Sprintf("%06d", n)
}
func (s *Server) sendCode(address, code string) error {
	if s.env == "development" && os.Getenv("DEV_EMAIL_LOG") == "true" {
		log.Printf("DEVELOPMENT EMAIL CODE for %s: %s", address, code)
		return nil
	}
	host := os.Getenv("SMTP_HOST")
	user := os.Getenv("SMTP_USER")
	pass := os.Getenv("SMTP_PASSWORD")
	from := os.Getenv("SMTP_FROM")
	port := env("SMTP_PORT", "587")
	if host == "" || user == "" || pass == "" || from == "" {
		return errors.New("SMTP is not configured")
	}
	body := fmt.Sprintf("To: %s\r\nFrom: %s\r\nSubject: 1609 SAT Olympiad verification code\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\nYour verification code is %s. It expires in 10 minutes.\r\n", address, from, code)
	client, err := smtp.Dial(host + ":" + port)
	if err != nil {
		return err
	}
	defer client.Close()
	if ok, _ := client.Extension("STARTTLS"); !ok {
		return errors.New("SMTP server does not support STARTTLS")
	}
	if err = client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
		return err
	}
	if err = client.Auth(smtp.PlainAuth("", user, pass, host)); err != nil {
		return err
	}
	if err = client.Mail(from); err != nil {
		return err
	}
	if err = client.Rcpt(address); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err = writer.Write([]byte(body)); err != nil {
		_ = writer.Close()
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}
func (s *Server) issueCode(r *http.Request, input codeRequest) (challenge, error) {
	var c challenge
	input.Email = normalizeEmail(input.Email)
	input.Name = strings.TrimSpace(input.Name)
	if len(input.Email) > 254 || !emailPattern.MatchString(input.Email) {
		return c, fail(400, "VALIDATION_ERROR", "Enter a valid email address.")
	}
	if input.Purpose != "sign-in" && input.Purpose != "sign-up" {
		return c, fail(400, "VALIDATION_ERROR", "Choose sign in or sign up.")
	}
	if input.Purpose == "sign-up" && (len(input.Name) < 2 || len(input.Name) > 100 || input.Grade < 7 || input.Grade > 12) {
		return c, fail(400, "VALIDATION_ERROR", "Enter your full name and a grade from 7 to 12.")
	}
	var count int
	e := s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM email_challenges WHERE email=$1 AND created_at>now()-interval '1 hour'`, input.Email).Scan(&count)
	if e != nil {
		return c, e
	}
	if count >= 8 {
		return c, fail(429, "RATE_LIMITED", "Too many codes requested. Try again later.")
	}
	code := randomCode()
	now := time.Now().UTC()
	c = challenge{newID(), input.Email, now.Add(10 * time.Minute), now.Add(30 * time.Second)}
	_, e = s.db.ExecContext(r.Context(), `INSERT INTO email_challenges(id,email,purpose,name,grade,code_hash,expires_at,resend_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, c.ID, input.Email, input.Purpose, input.Name, input.Grade, s.codeHash(c.ID, code), c.ExpiresAt, c.ResendAt)
	if e != nil {
		return c, e
	}
	if e = s.sendCode(c.Email, code); e != nil {
		_, _ = s.db.ExecContext(r.Context(), `DELETE FROM email_challenges WHERE id=$1`, c.ID)
		return challenge{}, e
	}
	return c, nil
}
func (s *Server) requestCode(w http.ResponseWriter, r *http.Request) error {
	var input codeRequest
	if e := decode(r, &input); e != nil {
		return e
	}
	c, e := s.issueCode(r, input)
	if e != nil {
		return e
	}
	ok(w, c)
	return nil
}
func (s *Server) resendCode(w http.ResponseWriter, r *http.Request) error {
	var input struct {
		ChallengeID string `json:"challengeId"`
	}
	if e := decode(r, &input); e != nil {
		return e
	}
	var old codeRequest
	var resend, expires time.Time
	var used sql.NullTime
	e := s.db.QueryRowContext(r.Context(), `SELECT purpose,email,name,grade,resend_at,expires_at,used_at FROM email_challenges WHERE id=$1`, input.ChallengeID).Scan(&old.Purpose, &old.Email, &old.Name, &old.Grade, &resend, &expires, &used)
	if errors.Is(e, sql.ErrNoRows) || used.Valid || time.Now().After(expires) {
		return fail(400, "EXPIRED_CODE", "Request a new code.")
	}
	if e != nil {
		return e
	}
	if time.Now().Before(resend) {
		return fail(429, "RATE_LIMITED", "Wait before requesting another code.")
	}
	_, e = s.db.ExecContext(r.Context(), `UPDATE email_challenges SET used_at=now() WHERE id=$1 AND used_at IS NULL`, input.ChallengeID)
	if e != nil {
		return e
	}
	c, e := s.issueCode(r, old)
	if e != nil {
		return e
	}
	ok(w, c)
	return nil
}
func (s *Server) verifyCode(w http.ResponseWriter, r *http.Request) error {
	var input struct {
		ChallengeID string `json:"challengeId"`
		Code        string `json:"code"`
	}
	if e := decode(r, &input); e != nil {
		return e
	}
	if len(input.Code) != 6 {
		return fail(400, "INVALID_CODE", "Enter the six-digit code.")
	}
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var c codeRequest
	var stored string
	var expires time.Time
	var used sql.NullTime
	var tries int
	e = tx.QueryRowContext(r.Context(), `SELECT purpose,email,name,grade,code_hash,expires_at,used_at,attempts FROM email_challenges WHERE id=$1 FOR UPDATE`, input.ChallengeID).Scan(&c.Purpose, &c.Email, &c.Name, &c.Grade, &stored, &expires, &used, &tries)
	if errors.Is(e, sql.ErrNoRows) || used.Valid || time.Now().After(expires) {
		return fail(400, "EXPIRED_CODE", "This code has expired. Request a new code.")
	}
	if e != nil {
		return e
	}
	if tries >= 5 {
		return fail(429, "RATE_LIMITED", "Too many incorrect attempts. Request a new code.")
	}
	if subtle.ConstantTimeCompare([]byte(stored), []byte(s.codeHash(input.ChallengeID, input.Code))) != 1 {
		_, e = tx.ExecContext(r.Context(), `UPDATE email_challenges SET attempts=attempts+1 WHERE id=$1`, input.ChallengeID)
		if e != nil {
			return e
		}
		if e = tx.Commit(); e != nil {
			return e
		}
		return fail(400, "INVALID_CODE", "That code is not valid.")
	}
	var u Student
	if c.Purpose == "sign-up" {
		newUser := newID()
		e = tx.QueryRowContext(r.Context(), `INSERT INTO users(id,name,grade,email) VALUES($1,$2,$3,$4) ON CONFLICT(email) DO UPDATE SET email=EXCLUDED.email RETURNING id,name,grade,email`, newUser, c.Name, c.Grade, c.Email).Scan(&u.ID, &u.Name, &u.Grade, &u.Email)
	} else {
		e = tx.QueryRowContext(r.Context(), `SELECT id,name,grade,email FROM users WHERE email=$1`, c.Email).Scan(&u.ID, &u.Name, &u.Grade, &u.Email)
	}
	if errors.Is(e, sql.ErrNoRows) {
		return fail(400, "VALIDATION_ERROR", "No account exists for this email. Create an account first.")
	}
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(r.Context(), `UPDATE email_challenges SET used_at=now() WHERE id=$1`, input.ChallengeID)
	if e != nil {
		return e
	}
	token := newID() + newID()
	_, e = tx.ExecContext(r.Context(), `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '30 days')`, hash(token), u.ID)
	if e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	http.SetCookie(w, &http.Cookie{Name: "sat_session", Value: token, Path: "/", HttpOnly: true, Secure: s.secureCookie, SameSite: s.cookieSameSite, Expires: time.Now().Add(30 * 24 * time.Hour)})
	ok(w, u)
	return nil
}
func (s *Server) me(w http.ResponseWriter, r *http.Request) error {
	ok(w, s.maybeStudent(r))
	return nil
}
func (s *Server) signOut(w http.ResponseWriter, r *http.Request) error {
	if c, e := r.Cookie("sat_session"); e == nil {
		_, _ = s.db.ExecContext(r.Context(), `DELETE FROM sessions WHERE token_hash=$1`, hash(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: "sat_session", Value: "", Path: "/", HttpOnly: true, Secure: s.secureCookie, SameSite: s.cookieSameSite, MaxAge: -1})
	ok(w, nil)
	return nil
}

var _ = strconv.Itoa
