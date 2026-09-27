package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"net/http"
	"sort"
	"strings"
	"time"
)

type answerValue struct {
	Kind     string `json:"kind"`
	ChoiceID string `json:"choiceId,omitempty"`
	Value    string `json:"value,omitempty"`
}
type highlight struct {
	ID      string `json:"id"`
	BlockID string `json:"blockId"`
	Start   int    `json:"start"`
	End     int    `json:"end"`
	Text    string `json:"text"`
	Color   string `json:"color"`
	Note    string `json:"note,omitempty"`
}
type answerTools struct {
	Notes               string      `json:"notes"`
	Highlights          []highlight `json:"highlights"`
	EliminatedChoiceIDs []string    `json:"eliminatedChoiceIds"`
}
type saveInput struct {
	Value            *answerValue `json:"value"`
	MarkedForReview  bool         `json:"markedForReview"`
	ExpectedRevision int          `json:"expectedRevision"`
	MutationID       string       `json:"mutationId"`
	Tools            *answerTools `json:"tools,omitempty"`
}

func (s *Server) saveAnswer(w http.ResponseWriter, r *http.Request) error {
	u, e := s.student(r)
	if e != nil {
		return e
	}
	var input saveInput
	if e = decode(r, &input); e != nil {
		return e
	}
	if input.MutationID == "" || input.ExpectedRevision < 0 {
		return fail(400, "VALIDATION_ERROR", "A request ID and revision are required.")
	}
	if input.Tools != nil {
		if len(input.Tools.Notes) > 10000 || len(input.Tools.Highlights) > 100 || len(input.Tools.EliminatedChoiceIDs) > 4 {
			return fail(400, "VALIDATION_ERROR", "Notes or annotations are too large.")
		}
	}
	var public []byte
	var sectionID string
	e = s.db.QueryRowContext(r.Context(), `SELECT public_json,section_id FROM questions WHERE id=$1 AND exam_id=$2`, r.PathValue("question"), s.examID).Scan(&public, &sectionID)
	if e != nil {
		return errDB(e)
	}
	var question struct {
		Kind    string `json:"kind"`
		Choices []struct {
			ID string `json:"id"`
		} `json:"choices"`
	}
	if e = json.Unmarshal(public, &question); e != nil {
		return e
	}
	if input.Value != nil {
		if question.Kind == "multiple-choice" && input.Value.Kind == "choice" {
			found := false
			for _, choice := range question.Choices {
				if choice.ID == input.Value.ChoiceID {
					found = true
				}
			}
			if !found {
				return fail(400, "VALIDATION_ERROR", "Unknown answer choice.")
			}
		} else if question.Kind == "numeric" && input.Value.Kind == "numeric" {
			if sectionID != "math" || len(input.Value.Value) > 32 || strings.TrimSpace(input.Value.Value) == "" {
				return fail(400, "VALIDATION_ERROR", "Invalid numeric response.")
			}
		} else {
			return fail(400, "VALIDATION_ERROR", "Answer type does not match this question.")
		}
	}
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	a, e := s.loadAttempt(r.Context(), tx, r.PathValue("attempt"), u.ID)
	if e != nil {
		return e
	}
	if a.Phase != "in-progress" || a.SectionID != sectionID {
		return fail(409, "CONFLICT", "This section is closed.")
	}
	var previousHash string
	var previous []byte
	e = tx.QueryRowContext(r.Context(), `SELECT payload_hash,response FROM mutations WHERE attempt_id=$1 AND mutation_id=$2`, a.ID, input.MutationID).Scan(&previousHash, &previous)
	if e == nil {
		body, _ := json.Marshal(input)
		if previousHash != hash(string(body)) {
			return fail(409, "CONFLICT", "This request ID was already used.")
		}
		if e = tx.Commit(); e != nil {
			return e
		}
		ok(w, json.RawMessage(previous))
		return nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	var current int
	e = tx.QueryRowContext(r.Context(), `SELECT revision FROM answers WHERE attempt_id=$1 AND question_id=$2 FOR UPDATE`, a.ID, r.PathValue("question")).Scan(&current)
	if errors.Is(e, sql.ErrNoRows) {
		current = 0
	} else if e != nil {
		return e
	}
	if current != input.ExpectedRevision {
		return fail(409, "CONFLICT", "A newer answer is saved. Refresh this section.")
	}
	value, e := json.Marshal(input.Value)
	if e != nil {
		return e
	}
	var tools []byte
	if input.Tools != nil {
		tools, e = json.Marshal(input.Tools)
		if e != nil {
			return e
		}
	}
	saved := time.Now().UTC()
	revision := current + 1
	_, e = tx.ExecContext(r.Context(), `INSERT INTO answers(attempt_id,question_id,value,marked_for_review,tools,revision,saved_at) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(attempt_id,question_id) DO UPDATE SET value=EXCLUDED.value,marked_for_review=EXCLUDED.marked_for_review,tools=EXCLUDED.tools,revision=EXCLUDED.revision,saved_at=EXCLUDED.saved_at`, a.ID, r.PathValue("question"), value, input.MarkedForReview, tools, revision, saved)
	if e != nil {
		return e
	}
	response := map[string]any{"questionId": r.PathValue("question"), "value": input.Value, "markedForReview": input.MarkedForReview, "revision": revision, "savedAt": saved}
	if input.Tools != nil {
		response["tools"] = input.Tools
	}
	responseJSON, _ := json.Marshal(response)
	body, _ := json.Marshal(input)
	_, e = tx.ExecContext(r.Context(), `INSERT INTO mutations(attempt_id,mutation_id,operation,payload_hash,response) VALUES($1,$2,'save-answer',$3,$4)`, a.ID, input.MutationID, hash(string(body)), responseJSON)
	if e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	ok(w, response)
	return nil
}

var eventKinds = map[string]bool{"tab-hidden": true, "window-blurred": true, "fullscreen-exited": true, "page-exit": true, "copy": true, "cut": true, "paste": true, "print": true, "context-menu": true, "developer-shortcut": true, "connection-lost": true, "connection-restored": true, "window-shrunk": true}
var focusKinds = map[string]bool{"tab-hidden": true, "window-blurred": true, "fullscreen-exited": true, "page-exit": true}

func (s *Server) violation(w http.ResponseWriter, r *http.Request) error {
	u, e := s.student(r)
	if e != nil {
		return e
	}
	var input struct {
		EventID        string    `json:"eventId"`
		Kind           string    `json:"kind"`
		ObservedAt     time.Time `json:"observedAt"`
		RelatedEventID string    `json:"relatedEventId,omitempty"`
	}
	if e = decode(r, &input); e != nil {
		return e
	}
	if input.EventID == "" || len(input.EventID) > 100 || !eventKinds[input.Kind] {
		return fail(400, "VALIDATION_ERROR", "Invalid browser event.")
	}
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	a, e := s.loadAttempt(r.Context(), tx, r.PathValue("attempt"), u.ID)
	if e != nil {
		return e
	}
	var originalKind, originalMessage string
	var previousCounted bool
	e = tx.QueryRowContext(r.Context(), `SELECT kind,counted,message FROM browser_events WHERE attempt_id=$1 AND event_id=$2`, a.ID, input.EventID).Scan(&originalKind, &previousCounted, &originalMessage)
	if e == nil {
		if originalKind != input.Kind {
			return fail(409, "CONFLICT", "Event ID already used for a different action.")
		}
		if e = tx.Commit(); e != nil {
			return e
		}
		ok(w, map[string]any{"counted": false, "message": "This event was already recorded.", "strikes": publicAttempt(a)["strikes"]})
		return nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if a.Phase != "in-progress" {
		return fail(409, "CONFLICT", "The sitting is not running.")
	}
	counted := true
	var prior bool
	if input.RelatedEventID != "" {
		e = tx.QueryRowContext(r.Context(), `SELECT true FROM browser_events WHERE attempt_id=$1 AND event_id=$2`, a.ID, input.RelatedEventID).Scan(&prior)
		if e == nil {
			counted = false
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
	}
	if counted && focusKinds[input.Kind] {
		e = tx.QueryRowContext(r.Context(), `SELECT true FROM browser_events WHERE attempt_id=$1 AND kind IN ('tab-hidden','window-blurred','fullscreen-exited','page-exit') AND received_at>now()-interval '3 seconds' LIMIT 1`, a.ID).Scan(&prior)
		if e == nil {
			counted = false
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
	}
	message := strings.ReplaceAll(input.Kind, "-", " ")
	if counted {
		a.EventCount++
		a.LastReason = sql.NullString{String: message, Valid: true}
		_, e = tx.ExecContext(r.Context(), `UPDATE attempts SET event_count=$2,last_event_reason=$3 WHERE id=$1`, a.ID, a.EventCount, message)
		if e != nil {
			return e
		}
	} else {
		message = "Related browser signals were grouped into one observation."
	}
	_, e = tx.ExecContext(r.Context(), `INSERT INTO browser_events(attempt_id,event_id,kind,observed_at,related_event_id,counted,message) VALUES($1,$2,$3,$4,$5,$6,$7)`, a.ID, input.EventID, input.Kind, input.ObservedAt, input.RelatedEventID, counted, message)
	if e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	ok(w, map[string]any{"counted": counted, "message": message, "strikes": publicAttempt(a)["strikes"]})
	return nil
}

type key struct {
	Kind     string   `json:"kind"`
	ChoiceID string   `json:"choiceId"`
	Value    string   `json:"value"`
	Accepted []string `json:"accepted"`
}

func canonicalNumber(v string) (*big.Rat, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, false
	}
	r := new(big.Rat)
	if _, ok := r.SetString(v); ok {
		return r, true
	}
	return nil, false
}
func matches(value *answerValue, k key) bool {
	if value == nil || value.Kind != k.Kind {
		return false
	}
	if k.Kind == "choice" {
		return value.ChoiceID == k.ChoiceID
	}
	if k.Kind == "numeric" {
		for _, candidate := range append([]string{k.Value}, k.Accepted...) {
			a, okA := canonicalNumber(value.Value)
			b, okB := canonicalNumber(candidate)
			if okA && okB && a.Cmp(b) == 0 {
				return true
			}
			if strings.TrimSpace(value.Value) == strings.TrimSpace(candidate) {
				return true
			}
		}
	}
	return false
}
func scaled(correct, total int) int {
	return 200 + 10*int(math.Round(float64(correct)*60/float64(total)))
}
func (s *Server) grade(ctx context.Context, tx *sql.Tx, a *attemptRow) error {
	rows, e := tx.QueryContext(ctx, `SELECT q.section_id,q.correct_answer,ans.value FROM questions q LEFT JOIN answers ans ON ans.question_id=q.id AND ans.attempt_id=$2 WHERE q.exam_id=$1`, a.ExamID, a.ID)
	if e != nil {
		return e
	}
	defer rows.Close()
	correct := map[string]int{}
	total := map[string]int{}
	for rows.Next() {
		var sectionID string
		var raw []byte
		var submitted []byte
		if e = rows.Scan(&sectionID, &raw, &submitted); e != nil {
			return e
		}
		total[sectionID]++
		var k key
		if e = json.Unmarshal(raw, &k); e != nil {
			return e
		}
		var v *answerValue
		if len(submitted) > 0 && string(submitted) != "null" {
			if e = json.Unmarshal(submitted, &v); e != nil {
				return e
			}
		}
		if matches(v, k) {
			correct[sectionID]++
		}
	}
	if e = rows.Err(); e != nil {
		return e
	}
	if e = rows.Close(); e != nil {
		return e
	}
	if total["reading-writing"] != 27 || total["math"] != 22 {
		return fail(503, "SERVICE_UNAVAILABLE", "Scoring is unavailable until the question bank is complete.")
	}
	a.ReadingScore = sql.NullInt64{Int64: int64(scaled(correct["reading-writing"], 27)), Valid: true}
	a.MathScore = sql.NullInt64{Int64: int64(scaled(correct["math"], 22)), Valid: true}
	_, e = tx.ExecContext(ctx, `UPDATE attempts SET reading_score=$2,math_score=$3 WHERE id=$1`, a.ID, a.ReadingScore.Int64, a.MathScore.Int64)
	return e
}

func (s *Server) releaseData(ctx context.Context) (map[string]any, error) {
	var explanation, leaderboard sql.NullTime
	e := s.db.QueryRowContext(ctx, `SELECT explanations_released_at,leaderboard_released_at FROM exams WHERE id=$1`, s.examID).Scan(&explanation, &leaderboard)
	if e != nil {
		return nil, e
	}
	state := func(v sql.NullTime) map[string]any {
		if v.Valid && !time.Now().Before(v.Time) {
			return map[string]any{"status": "released", "releasedAt": v.Time}
		}
		return map[string]any{"status": "locked", "message": "Results have not been released."}
	}
	return map[string]any{"explanations": state(explanation), "leaderboard": state(leaderboard)}, nil
}
func (s *Server) releases(w http.ResponseWriter, r *http.Request) error {
	if e := s.requireExam(r); e != nil {
		return e
	}
	if _, e := s.student(r); e != nil {
		return e
	}
	data, e := s.releaseData(r.Context())
	if e != nil {
		return e
	}
	ok(w, data)
	return nil
}
func (s *Server) result(w http.ResponseWriter, r *http.Request) error {
	a, e := s.owned(r)
	if e != nil {
		return e
	}
	if a.Phase != "completed" {
		return fail(409, "CONFLICT", "The exam is not complete.")
	}
	if !a.ReadingScore.Valid || !a.MathScore.Valid {
		return fail(503, "SERVICE_UNAVAILABLE", "The result is still being calculated.")
	}
	release, e := s.releaseData(r.Context())
	if e != nil {
		return e
	}
	elapsed := int(a.Completed.Time.Sub(a.FirstStarted.Time).Seconds())
	if elapsed < 0 {
		elapsed = 0
	}
	score := func(value, max int) map[string]any {
		return map[string]any{"value": value, "maximum": max, "label": "Independent 1609 Olympiad scale"}
	}
	ok(w, map[string]any{"attemptId": a.ID, "submittedAt": a.Completed.Time, "timeTakenSeconds": elapsed, "readingWriting": score(int(a.ReadingScore.Int64), 800), "math": score(int(a.MathScore.Int64), 800), "overall": score(int(a.ReadingScore.Int64+a.MathScore.Int64), 1600), "releases": release})
	return nil
}
func (s *Server) review(w http.ResponseWriter, r *http.Request) error {
	a, e := s.owned(r)
	if e != nil {
		return e
	}
	if a.Phase != "completed" {
		return fail(409, "CONFLICT", "The exam is not complete.")
	}
	release, e := s.releaseData(r.Context())
	if e != nil {
		return e
	}
	state := release["explanations"].(map[string]any)
	if state["status"] == "locked" {
		ok(w, state)
		return nil
	}
	rows, e := s.db.QueryContext(r.Context(), `SELECT q.public_json,q.correct_answer,q.explanation,ans.value FROM questions q LEFT JOIN answers ans ON ans.question_id=q.id AND ans.attempt_id=$2 WHERE q.exam_id=$1 ORDER BY CASE q.section_id WHEN 'reading-writing' THEN 1 ELSE 2 END,q.position`, a.ExamID, a.ID)
	if e != nil {
		return e
	}
	defer rows.Close()
	items := []any{}
	for rows.Next() {
		var q, k, explanation, submitted []byte
		if e = rows.Scan(&q, &k, &explanation, &submitted); e != nil {
			return e
		}
		if len(submitted) == 0 {
			submitted = []byte("null")
		}
		items = append(items, map[string]any{"question": json.RawMessage(q), "submittedAnswer": json.RawMessage(submitted), "correctAnswer": json.RawMessage(k), "explanation": json.RawMessage(explanation)})
	}
	if e = rows.Err(); e != nil {
		return e
	}
	ok(w, map[string]any{"status": "released", "releasedAt": state["releasedAt"], "data": map[string]any{"attemptId": a.ID, "items": items}})
	return nil
}
func (s *Server) leaderboard(w http.ResponseWriter, r *http.Request) error {
	if e := s.requireExam(r); e != nil {
		return e
	}
	if _, e := s.student(r); e != nil {
		return e
	}
	rows, e := s.db.QueryContext(r.Context(), `SELECT id,name,grade FROM users ORDER BY name,id`)
	if e != nil {
		return e
	}
	participants := []any{}
	for rows.Next() {
		var id, name string
		var grade int
		if e = rows.Scan(&id, &name, &grade); e != nil {
			rows.Close()
			return e
		}
		participants = append(participants, map[string]any{"id": id, "name": name, "grade": grade})
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	release, e := s.releaseData(r.Context())
	if e != nil {
		return e
	}
	state := release["leaderboard"].(map[string]any)
	if state["status"] == "locked" {
		ok(w, map[string]any{"participants": participants, "results": state})
		return nil
	}
	resultRows, e := s.db.QueryContext(r.Context(), `SELECT u.name,u.grade,a.reading_score+a.math_score,EXTRACT(EPOCH FROM (a.completed_at-a.first_started_at))::integer FROM attempts a JOIN users u ON u.id=a.user_id WHERE a.exam_id=$1 AND a.phase='completed' AND a.reading_score IS NOT NULL AND a.math_score IS NOT NULL ORDER BY a.reading_score+a.math_score DESC,a.completed_at-a.first_started_at ASC,u.name`, s.examID)
	if e != nil {
		return e
	}
	defer resultRows.Close()
	type result struct {
		Name                  string
		Grade, Score, Seconds int
	}
	all := []result{}
	for resultRows.Next() {
		var x result
		if e = resultRows.Scan(&x.Name, &x.Grade, &x.Score, &x.Seconds); e != nil {
			return e
		}
		all = append(all, x)
	}
	if e = resultRows.Err(); e != nil {
		return e
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Score != all[j].Score {
			return all[i].Score > all[j].Score
		}
		return all[i].Seconds < all[j].Seconds
	})
	entries := []any{}
	for i, x := range all {
		entries = append(entries, map[string]any{"rank": i + 1, "name": x.Name, "grade": x.Grade, "score": map[string]any{"value": x.Score, "maximum": 1600, "label": "Independent 1609 Olympiad scale"}, "timeTakenSeconds": x.Seconds})
	}
	ok(w, map[string]any{"participants": participants, "results": map[string]any{"status": "released", "releasedAt": state["releasedAt"], "data": entries}})
	return nil
}
