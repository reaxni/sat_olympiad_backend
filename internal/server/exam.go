package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"satbackend/internal/exammedia"
	"strings"
	"time"
)

type section struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	Difficulty      string `json:"difficulty"`
	QuestionCount   int    `json:"questionCount"`
	DurationSeconds int    `json:"durationSeconds"`
}

var sections = []section{{"reading-writing", "Reading and Writing", "hard", 27, 1920}, {"math", "Math", "hard", 22, 2100}}

const eventLimit = 5

// Guard the public API even if a question bank was imported by an older admin tool.
func publicQuestionSafe(raw []byte) bool {
	var tree any
	if json.Unmarshal(raw, &tree) != nil {
		return false
	}
	var walk func(any) bool
	walk = func(value any) bool {
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(key))
				switch normalized {
				case "correctanswer", "answerkey", "iscorrect", "explanation", "solution", "accepted", "grading":
					return false
				}
				if !walk(child) {
					return false
				}
			}
		case []any:
			for _, child := range typed {
				if !walk(child) {
					return false
				}
			}
		}
		return true
	}
	return walk(tree)
}

type attemptRow struct {
	ID, ExamID, UserID, Phase, SectionID                     string
	Started, Deadline, FirstStarted, Completed, Disqualified sql.NullTime
	EventCount                                               int
	LastReason                                               sql.NullString
	ReadingScore, MathScore                                  sql.NullInt64
}

func scanAttempt(row interface{ Scan(dest ...any) error }) (attemptRow, error) {
	var a attemptRow
	e := row.Scan(&a.ID, &a.ExamID, &a.UserID, &a.Phase, &a.SectionID, &a.Started, &a.Deadline, &a.FirstStarted, &a.Completed, &a.Disqualified, &a.EventCount, &a.LastReason, &a.ReadingScore, &a.MathScore)
	return a, e
}

const attemptColumns = `id,exam_id,user_id,phase,section_id,started_at,deadline_at,first_started_at,completed_at,disqualified_at,event_count,last_event_reason,reading_score,math_score`

func publicAttempt(a attemptRow) map[string]any {
	progress := map[string]any{"phase": a.Phase}
	switch a.Phase {
	case "instructions":
		progress["sectionId"] = a.SectionID
	case "in-progress":
		progress["sectionId"] = a.SectionID
		progress["startedAt"] = a.Started.Time
		progress["deadlineAt"] = a.Deadline.Time
	case "completed":
		progress["completedAt"] = a.Completed.Time
	case "disqualified":
		progress["disqualifiedAt"] = a.Disqualified.Time
	}
	remaining := eventLimit - a.EventCount
	if remaining < 0 {
		remaining = 0
	}
	strikes := map[string]any{"count": a.EventCount, "limit": eventLimit, "remaining": remaining, "disqualified": a.Phase == "disqualified"}
	if a.LastReason.Valid {
		strikes["lastReason"] = a.LastReason.String
	}
	return map[string]any{"id": a.ID, "examId": a.ExamID, "progress": progress, "strikes": strikes}
}
func (s *Server) schedule(w http.ResponseWriter, r *http.Request) error {
	if _, e := s.student(r); e != nil {
		return e
	}
	ok(w, map[string]any{"id": s.examID, "title": s.examTitle, "opensAt": s.openAt, "entryClosesAt": s.closeAt, "sections": sections, "autoSubmitAfterEvents": eventLimit})
	return nil
}
func (s *Server) questionBankReady(ctx context.Context) (bool, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT section_id,count(*) FROM questions WHERE exam_id=$1 GROUP BY section_id`, s.examID)
	if e != nil {
		return false, e
	}
	defer rows.Close()
	count := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if e = rows.Scan(&id, &n); e != nil {
			return false, e
		}
		count[id] = n
	}
	return count["reading-writing"] == 27 && count["math"] == 22, rows.Err()
}
func (s *Server) eligibilityData(ctx context.Context, userID string) (map[string]any, error) {
	now := time.Now().UTC()
	block := func(reason, message string) map[string]any {
		return map[string]any{"status": "blocked", "reason": reason, "message": message}
	}
	if now.Before(s.openAt) {
		return block("not-open", "The exam has not opened yet."), nil
	}
	if !now.Before(s.closeAt) {
		return block("entry-closed", "The entry window has closed."), nil
	}
	var phase string
	e := s.db.QueryRowContext(ctx, `SELECT phase FROM attempts WHERE exam_id=$1 AND user_id=$2`, s.examID, userID).Scan(&phase)
	if e == nil {
		if phase == "completed" {
			return block("already-completed", "You have already completed this exam."), nil
		}
		if phase == "disqualified" {
			return block("disqualified", "This attempt was restricted after five confirmed browser events."), nil
		}
		return map[string]any{"status": "eligible"}, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return nil, e
	}
	ready, e := s.questionBankReady(ctx)
	if e != nil {
		return nil, e
	}
	if !ready {
		return block("not-open", "The exam question bank is not ready. Contact the organizer."), nil
	}
	return map[string]any{"status": "eligible"}, nil
}
func (s *Server) eligibility(w http.ResponseWriter, r *http.Request) error {
	if e := s.requireExam(r); e != nil {
		return e
	}
	u, e := s.student(r)
	if e != nil {
		return e
	}
	v, e := s.eligibilityData(r.Context(), u.ID)
	if e != nil {
		return e
	}
	ok(w, v)
	return nil
}
func (s *Server) loadAttempt(ctx context.Context, tx *sql.Tx, id, userID string) (attemptRow, error) {
	a, e := scanAttempt(tx.QueryRowContext(ctx, `SELECT `+attemptColumns+` FROM attempts WHERE id=$1 AND user_id=$2 FOR UPDATE`, id, userID))
	if e != nil {
		return a, errDB(e)
	}
	return s.advance(ctx, tx, a)
}
func (s *Server) advance(ctx context.Context, tx *sql.Tx, a attemptRow) (attemptRow, error) {
	if a.Phase == "in-progress" && a.EventCount >= eventLimit {
		a.Phase = "disqualified"
		a.Disqualified = sql.NullTime{Time: time.Now().UTC(), Valid: true}
		_, e := tx.ExecContext(ctx, `UPDATE attempts SET phase='disqualified',disqualified_at=$2 WHERE id=$1`, a.ID, a.Disqualified.Time)
		return a, e
	}
	var closes time.Time
	if a.Phase == "in-progress" || (a.Phase == "instructions" && a.FirstStarted.Valid) {
		if e := tx.QueryRowContext(ctx, `SELECT entry_closes_at FROM exams WHERE id=$1`, a.ExamID).Scan(&closes); e != nil {
			return a, e
		}
		if !time.Now().UTC().Before(closes) {
			// Preserve an earlier natural finish if the worker was offline.
			completed := closes
			if a.Deadline.Valid {
				natural := a.Deadline.Time
				if a.SectionID == "reading-writing" {
					natural = natural.Add(35 * time.Minute)
				}
				completed = boundedDeadline(natural, closes)
			}
			return s.finishAttempt(ctx, tx, a, completed)
		}
	}
	if a.Phase != "in-progress" || !a.Deadline.Valid {
		return a, nil
	}
	now := time.Now().UTC()
	for a.Phase == "in-progress" && !now.Before(a.Deadline.Time) {
		if a.SectionID == "reading-writing" {
			start := a.Deadline.Time
			a.SectionID = "math"
			a.Started = sql.NullTime{Time: start, Valid: true}
			a.Deadline = sql.NullTime{Time: boundedDeadline(start.Add(35*time.Minute), closes), Valid: true}
			_, e := tx.ExecContext(ctx, `UPDATE attempts SET section_id='math',started_at=$2,deadline_at=$3 WHERE id=$1`, a.ID, a.Started.Time, a.Deadline.Time)
			if e != nil {
				return a, e
			}
		} else {
			a.Phase = "completed"
			a.Completed = sql.NullTime{Time: a.Deadline.Time, Valid: true}
			_, e := tx.ExecContext(ctx, `UPDATE attempts SET phase='completed',completed_at=$2 WHERE id=$1`, a.ID, a.Completed.Time)
			if e != nil {
				return a, e
			}
			if e = s.grade(ctx, tx, &a); e != nil {
				return a, e
			}
		}
	}
	return a, nil
}

func boundedDeadline(deadline, closes time.Time) time.Time {
	if closes.Before(deadline) {
		return closes
	}
	return deadline
}

func (s *Server) finishAttempt(ctx context.Context, tx *sql.Tx, a attemptRow, completed time.Time) (attemptRow, error) {
	a.Phase = "completed"
	a.Completed = sql.NullTime{Time: completed, Valid: true}
	if _, e := tx.ExecContext(ctx, `UPDATE attempts SET phase='completed',completed_at=$2 WHERE id=$1`, a.ID, completed); e != nil {
		return a, e
	}
	e := s.grade(ctx, tx, &a)
	return a, e
}
func (s *Server) owned(r *http.Request) (attemptRow, error) {
	u, e := s.student(r)
	if e != nil {
		return attemptRow{}, e
	}
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		return attemptRow{}, e
	}
	defer tx.Rollback()
	a, e := s.loadAttempt(r.Context(), tx, r.PathValue("attempt"), u.ID)
	if e != nil {
		return a, e
	}
	return a, tx.Commit()
}
func (s *Server) activeAttempt(w http.ResponseWriter, r *http.Request) error {
	if e := s.requireExam(r); e != nil {
		return e
	}
	u, e := s.student(r)
	if e != nil {
		return e
	}
	var id string
	e = s.db.QueryRowContext(r.Context(), `SELECT id FROM attempts WHERE exam_id=$1 AND user_id=$2`, s.examID, u.ID).Scan(&id)
	if errors.Is(e, sql.ErrNoRows) {
		ok(w, nil)
		return nil
	}
	if e != nil {
		return e
	}
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	a, e := s.loadAttempt(r.Context(), tx, id, u.ID)
	if e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	ok(w, publicAttempt(a))
	return nil
}
func (s *Server) createAttempt(w http.ResponseWriter, r *http.Request) error {
	if e := s.requireExam(r); e != nil {
		return e
	}
	u, e := s.student(r)
	if e != nil {
		return e
	}
	var input struct {
		MutationID string `json:"mutationId"`
	}
	if e = decode(r, &input); e != nil {
		return e
	}
	if input.MutationID == "" {
		return fail(400, "VALIDATION_ERROR", "A request ID is required.")
	}
	allowed, e := s.eligibilityData(r.Context(), u.ID)
	if e != nil {
		return e
	}
	if allowed["status"] != "eligible" {
		return fail(403, "FORBIDDEN", allowed["message"].(string))
	}
	id := newID()
	_, e = s.db.ExecContext(r.Context(), `INSERT INTO attempts(id,exam_id,user_id) VALUES($1,$2,$3) ON CONFLICT(exam_id,user_id) DO NOTHING`, id, s.examID, u.ID)
	if e != nil {
		return e
	}
	e = s.db.QueryRowContext(r.Context(), `SELECT id FROM attempts WHERE exam_id=$1 AND user_id=$2`, s.examID, u.ID).Scan(&id)
	if e != nil {
		return e
	}
	r.SetPathValue("attempt", id)
	a, e := s.owned(r)
	if e != nil {
		return e
	}
	ok(w, publicAttempt(a))
	return nil
}
func (s *Server) getAttempt(w http.ResponseWriter, r *http.Request) error {
	a, e := s.owned(r)
	if e != nil {
		return e
	}
	ok(w, publicAttempt(a))
	return nil
}
func validSection(id string) bool { return id == "reading-writing" || id == "math" }
func (s *Server) startSection(w http.ResponseWriter, r *http.Request) error {
	u, e := s.student(r)
	if e != nil {
		return e
	}
	sectionID := r.PathValue("section")
	if !validSection(sectionID) {
		return fail(404, "NOT_FOUND", "Section not found.")
	}
	var input struct {
		MutationID string `json:"mutationId"`
	}
	if e = decode(r, &input); e != nil {
		return e
	}
	if input.MutationID == "" {
		return fail(400, "VALIDATION_ERROR", "A request ID is required.")
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
	if a.Phase == "instructions" && a.SectionID == sectionID {
		now := time.Now().UTC()
		if now.Before(s.openAt) || !now.Before(s.closeAt) {
			return fail(403, "FORBIDDEN", "The entry window is closed.")
		}
		duration := 32 * time.Minute
		if sectionID == "math" {
			duration = 35 * time.Minute
		}
		a.Phase = "in-progress"
		a.Started = sql.NullTime{Time: now, Valid: true}
		a.Deadline = sql.NullTime{Time: boundedDeadline(now.Add(duration), s.closeAt), Valid: true}
		if !a.FirstStarted.Valid {
			a.FirstStarted = a.Started
		}
		_, e = tx.ExecContext(r.Context(), `UPDATE attempts SET phase='in-progress',started_at=$2,deadline_at=$3,first_started_at=coalesce(first_started_at,$2) WHERE id=$1`, a.ID, now, a.Deadline.Time)
		if e != nil {
			return e
		}
	} else if a.Phase != "in-progress" || a.SectionID != sectionID {
		if e = tx.Commit(); e != nil {
			return e
		}
		return fail(403, "FORBIDDEN", "This section cannot be started.")
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	ok(w, publicAttempt(a))
	return nil
}
func (s *Server) getSection(w http.ResponseWriter, r *http.Request) error {
	a, e := s.owned(r)
	if e != nil {
		return e
	}
	sectionID := r.PathValue("section")
	if a.Phase != "in-progress" || a.SectionID != sectionID {
		return fail(409, "CONFLICT", "This section is closed. Refresh the attempt.")
	}
	var sec section
	for _, v := range sections {
		if v.ID == sectionID {
			sec = v
		}
	}
	if sec.ID == "" {
		return fail(404, "NOT_FOUND", "Section not found.")
	}
	assets, e := s.loadAssets(r.Context(), a.ExamID, sectionID)
	if e != nil {
		return e
	}
	rows, e := s.db.QueryContext(r.Context(), `SELECT id,position,public_json FROM questions WHERE exam_id=$1 AND section_id=$2 ORDER BY position`, a.ExamID, sectionID)
	if e != nil {
		return e
	}
	defer rows.Close()
	slots := make([]any, 0, sec.QuestionCount)
	index := map[int]json.RawMessage{}
	for rows.Next() {
		var pos int
		var questionID string
		var q json.RawMessage
		if e = rows.Scan(&questionID, &pos, &q); e != nil {
			return e
		}
		if !publicQuestionSafe(q) {
			return fail(503, "SERVICE_UNAVAILABLE", "The exam questions need organizer review.")
		}
		q, e = exammedia.Hydrate(q, assets[questionID])
		if e != nil {
			return e
		}
		index[pos] = q
	}
	if e = rows.Err(); e != nil {
		return e
	}
	for i := 1; i <= sec.QuestionCount; i++ {
		var q any = nil
		if v, yes := index[i]; yes {
			q = v
		}
		slots = append(slots, map[string]any{"position": i, "question": q})
	}
	if len(index) != sec.QuestionCount {
		return fail(503, "SERVICE_UNAVAILABLE", "The exam questions are incomplete.")
	}
	answerRows, e := s.db.QueryContext(r.Context(), `SELECT a.question_id,a.value,a.marked_for_review,a.revision,a.saved_at,a.tools FROM answers a JOIN questions q ON q.id=a.question_id WHERE a.attempt_id=$1 AND q.section_id=$2 ORDER BY q.position`, a.ID, sectionID)
	if e != nil {
		return e
	}
	defer answerRows.Close()
	answers := []any{}
	for answerRows.Next() {
		var qid string
		var value, tools []byte
		var marked bool
		var rev int
		var saved time.Time
		if e = answerRows.Scan(&qid, &value, &marked, &rev, &saved, &tools); e != nil {
			return e
		}
		entry := map[string]any{"questionId": qid, "value": json.RawMessage(value), "markedForReview": marked, "revision": rev, "savedAt": saved}
		if len(tools) > 0 {
			entry["tools"] = json.RawMessage(tools)
		}
		answers = append(answers, entry)
	}
	if e = answerRows.Err(); e != nil {
		return e
	}
	ok(w, map[string]any{"section": sec, "slots": slots, "answers": answers})
	return nil
}
func (s *Server) submitSection(w http.ResponseWriter, r *http.Request) error {
	u, e := s.student(r)
	if e != nil {
		return e
	}
	var input struct {
		MutationID string `json:"mutationId"`
	}
	if e = decode(r, &input); e != nil {
		return e
	}
	if input.MutationID == "" {
		return fail(400, "VALIDATION_ERROR", "A request ID is required.")
	}
	sectionID := r.PathValue("section")
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	a, e := s.loadAttempt(r.Context(), tx, r.PathValue("attempt"), u.ID)
	if e != nil {
		return e
	}
	if a.Phase == "in-progress" && a.SectionID == sectionID {
		now := time.Now().UTC()
		if sectionID == "reading-writing" {
			a.SectionID = "math"
			a.Started = sql.NullTime{Time: now, Valid: true}
			a.Deadline = sql.NullTime{Time: boundedDeadline(now.Add(35*time.Minute), s.closeAt), Valid: true}
			_, e = tx.ExecContext(r.Context(), `UPDATE attempts SET section_id='math',started_at=$2,deadline_at=$3 WHERE id=$1`, a.ID, now, a.Deadline.Time)
		} else if sectionID == "math" {
			a.Phase = "completed"
			a.Completed = sql.NullTime{Time: now, Valid: true}
			_, e = tx.ExecContext(r.Context(), `UPDATE attempts SET phase='completed',completed_at=$2 WHERE id=$1`, a.ID, now)
			if e == nil {
				e = s.grade(r.Context(), tx, &a)
			}
		} else {
			return fail(404, "NOT_FOUND", "Section not found.")
		}
		if e != nil {
			return e
		}
	} else if a.Phase != "completed" && !(sectionID == "reading-writing" && a.SectionID == "math") {
		return fail(409, "CONFLICT", "This section is not active.")
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	ok(w, publicAttempt(a))
	return nil
}

func (s *Server) reconcileLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if _, e := s.releaseData(ctx); e != nil {
			log.Printf("deadline reconciliation: %v", e)
		}
		cancel()
		<-ticker.C
	}
}

func (s *Server) reconcileDue(ctx context.Context, examID string) error {
	rows, e := s.db.QueryContext(ctx, `SELECT a.id,a.user_id FROM attempts a JOIN exams e ON e.id=a.exam_id WHERE a.exam_id=$1 AND ((a.phase='in-progress' AND a.deadline_at<=$2) OR (a.phase IN ('in-progress','instructions') AND a.first_started_at IS NOT NULL AND e.entry_closes_at<=$2)) ORDER BY a.id`, examID, time.Now().UTC())
	if e != nil {
		return e
	}
	type due struct{ id, user string }
	items := []due{}
	for rows.Next() {
		var item due
		if e = rows.Scan(&item.id, &item.user); e != nil {
			rows.Close()
			return e
		}
		items = append(items, item)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, item := range items {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		_, err = s.loadAttempt(ctx, tx, item.id, item.user)
		if err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		if err != nil {
			return fmt.Errorf("deadline update %s: %w", item.id, err)
		}
	}
	return nil
}
