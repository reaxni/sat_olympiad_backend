package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// Uses an isolated schema, never the application's existing tables.
func TestExamClosePostgres(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("Set TEST_DATABASE_URL for PostgreSQL deadline integration tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	schema := "deadline_test_" + newID()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE SCHEMA ` + schema)
	defer db.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
	exec(`SET search_path TO ` + schema)
	migration, err := os.ReadFile("../../migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	exec(string(migration))
	ctx := context.Background()
	closeAt := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	s := &Server{db: db, examID: "deadline-exam", closeAt: closeAt}
	exec(`INSERT INTO exams(id,title,opens_at,entry_closes_at) VALUES($1,'Deadline exam',$2,$3)`, s.examID, closeAt.Add(-2*time.Hour), closeAt)
	for _, sec := range sections {
		for i := 1; i <= sec.QuestionCount; i++ {
			exec(`INSERT INTO questions(id,exam_id,section_id,position,public_json,correct_answer,difficulty) VALUES($1,$2,$3,$4,$5,$6,'hard')`, fmt.Sprintf("%s-%d", sec.ID, i), s.examID, sec.ID, i, `{"kind":"multiple-choice","choices":[{"id":"A"},{"id":"B"}]}`, `{"kind":"choice","choiceId":"A"}`)
		}
	}
	// Even an old/manual release timestamp cannot reveal ranks before closing.
	exec(`UPDATE exams SET leaderboard_released_at=now()-interval '1 hour' WHERE id=$1`, s.examID)
	release, err := s.releaseData(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if release["leaderboard"].(map[string]any)["status"] != "locked" {
		t.Fatal("ranking exposed before closing")
	}
	closeAt = time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	s.closeAt = closeAt
	exec(`UPDATE exams SET entry_closes_at=$2 WHERE id=$1`, s.examID, closeAt)
	for _, id := range []string{"reading", "math", "between", "unstarted", "restricted", "natural"} {
		exec(`INSERT INTO users(id,name,grade,email) VALUES($1,$1,10,$2)`, id, id+"@example.test")
		exec(`INSERT INTO attempts(id,exam_id,user_id) VALUES($1,$2,$1)`, id, s.examID)
	}
	for _, id := range []string{"reading", "math", "between", "natural"} {
		sectionID := "math"
		phase := "in-progress"
		if id == "reading" {
			sectionID = "reading-writing"
		}
		if id == "between" {
			phase = "instructions"
		}
		deadline := closeAt.Add(time.Hour)
		if id == "natural" {
			deadline = closeAt.Add(-time.Minute)
		}
		exec(`UPDATE attempts SET phase=$2,section_id=$3,started_at=$4,first_started_at=$4,deadline_at=$5 WHERE id=$1`, id, phase, sectionID, closeAt.Add(-10*time.Minute), deadline)
	}
	exec(`UPDATE attempts SET phase='disqualified',event_count=5 WHERE id='restricted'`)
	for _, answer := range []struct{ attempt, question string }{{"reading", "reading-writing-1"}, {"math", "reading-writing-1"}, {"math", "reading-writing-2"}, {"math", "math-1"}} {
		exec(`INSERT INTO answers(attempt_id,question_id,value) VALUES($1,$2,'{"kind":"choice","choiceId":"A"}')`, answer.attempt, answer.question)
	}
	exec(`INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,'reading',now()+interval '1 hour')`, hash("deadline-token"))
	request := func(method, path, body string) *http.Request {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "sat_session", Value: "deadline-token"})
		r.SetPathValue("attempt", "reading")
		r.SetPathValue("question", "reading-writing-1")
		r.SetPathValue("exam", s.examID)
		return r
	}
	// A late save must commit finalization while rejecting the new answer.
	err = s.saveAnswer(httptest.NewRecorder(), request("PUT", "/answer", `{"value":{"kind":"choice","choiceId":"B"},"expectedRevision":1,"mutationId":"late"}`))
	if err == nil {
		t.Fatal("late answer accepted")
	}
	a, err := scanAttempt(db.QueryRow(`SELECT ` + attemptColumns + ` FROM attempts WHERE id='reading'`))
	if err != nil {
		t.Fatal(err)
	}
	if a.Phase != "completed" || a.ReadingScore.Int64 != 220 || a.MathScore.Int64 != 200 || !a.Completed.Time.Equal(closeAt) {
		t.Fatalf("late save did not finalize correctly: %+v", a)
	}
	var value []byte
	if err = db.QueryRow(`SELECT value FROM answers WHERE attempt_id='reading'`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(value), `"A"`) {
		t.Fatal("saved answer overwritten after deadline")
	}
	// The ranking request itself finalizes absent browsers before publishing ranks.
	w := httptest.NewRecorder()
	if err = s.leaderboard(w, request("GET", "/leaderboard", "")); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Data struct {
			Results struct {
				Status string
				Data   []struct {
					Rank  int
					Name  string
					Score struct{ Value int }
				}
			}
		}
	}
	if err = json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	entries := payload.Data.Results.Data
	if payload.Data.Results.Status != "released" || len(entries) != 4 {
		t.Fatalf("ranking missing: %s", w.Body.String())
	}
	if entries[0].Rank != 1 || entries[0].Name != "math" || entries[0].Score.Value != 470 || entries[1].Name != "reading" || entries[1].Rank != 2 || entries[2].Name != "natural" || entries[2].Rank != 3 {
		t.Fatalf("incorrect ranking: %+v", entries)
	}
	if strings.Contains(w.Body.String(), "@example.test") {
		t.Fatal("ranking leaked email")
	}
	for _, id := range []string{"math", "between", "natural"} {
		a, err = scanAttempt(db.QueryRow(`SELECT `+attemptColumns+` FROM attempts WHERE id=$1`, id))
		if err != nil {
			t.Fatal(err)
		}
		expected := closeAt
		if id == "natural" {
			expected = expected.Add(-time.Minute)
		}
		if a.Phase != "completed" || !a.Completed.Time.Equal(expected) || !a.MathScore.Valid {
			t.Fatalf("not finalized: %+v", a)
		}
	}
	// Repeating reconciliation leaves scores/timestamps stable and never starts nonparticipants.
	if _, err = s.releaseData(ctx); err != nil {
		t.Fatal(err)
	}
	var phase string
	if err = db.QueryRow(`SELECT phase FROM attempts WHERE id='unstarted'`).Scan(&phase); err != nil || phase != "instructions" {
		t.Fatal("unstarted attempt was scored")
	}
	if err = db.QueryRow(`SELECT phase FROM attempts WHERE id='restricted'`).Scan(&phase); err != nil || phase != "disqualified" {
		t.Fatal("restricted attempt changed")
	}
	var explanation sql.NullTime
	if err = db.QueryRow(`SELECT explanations_released_at FROM exams WHERE id=$1`, s.examID).Scan(&explanation); err != nil || explanation.Valid {
		t.Fatal("answer keys must stay locked")
	}
}
