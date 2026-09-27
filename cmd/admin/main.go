package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	_ "github.com/jackc/pgx/v5/stdlib"
	"os"
	"strings"
	"time"
)

type item struct {
	Public        json.RawMessage `json:"public"`
	CorrectAnswer json.RawMessage `json:"correctAnswer"`
	Explanation   json.RawMessage `json:"explanation"`
}
type bank struct {
	Questions []item `json:"questions"`
}
type question struct {
	ID        string `json:"id"`
	SectionID string `json:"sectionId"`
	Position  int    `json:"position"`
	Kind      string `json:"kind"`
	Choices   []struct {
		ID string `json:"id"`
	} `json:"choices"`
}
type key struct {
	Kind     string `json:"kind"`
	ChoiceID string `json:"choiceId"`
	Value    string `json:"value"`
}

func validateMedia(v any) error {
	switch x := v.(type) {
	case map[string]any:
		if x["kind"] == "image" {
			url, _ := x["url"].(string)
			if !(strings.HasPrefix(url, "data:image/png;base64,") || strings.HasPrefix(url, "data:image/jpeg;base64,") || strings.HasPrefix(url, "data:image/svg+xml;base64,")) {
				return errors.New("question images must be embedded data URIs so unreleased media stays gated")
			}
		}
		for _, child := range x {
			if e := validateMedia(child); e != nil {
				return e
			}
		}
	case []any:
		for _, child := range x {
			if e := validateMedia(child); e != nil {
				return e
			}
		}
	}
	return nil
}

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 3 {
		return errors.New("usage: admin import <bank.json> | release <explanations|leaderboard> | lock <attempt-id> <reason>")
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL is required")
	}
	db, e := sql.Open("pgx", dsn)
	if e != nil {
		return e
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if e = db.PingContext(ctx); e != nil {
		return e
	}
	examID := os.Getenv("EXAM_ID")
	if examID == "" {
		examID = "1609-olympiad"
	}
	switch os.Args[1] {
	case "import":
		return importBank(ctx, db, examID, os.Args[2])
	case "release":
		column := ""
		switch os.Args[2] {
		case "explanations":
			column = "explanations_released_at"
		case "leaderboard":
			column = "leaderboard_released_at"
		default:
			return errors.New("release target must be explanations or leaderboard")
		}
		_, e = db.ExecContext(ctx, `UPDATE exams SET `+column+`=now() WHERE id=$1`, examID)
		return e
	case "lock":
		if len(os.Args) < 4 || strings.TrimSpace(os.Args[3]) == "" {
			return errors.New("lock requires an attempt id and organizer reason")
		}
		res, e := db.ExecContext(ctx, `UPDATE attempts SET phase='disqualified',disqualified_at=now(),disqualification_reason=$2,last_event_reason=$2 WHERE id=$1 AND exam_id=$3 AND phase<>'completed'`, os.Args[2], os.Args[3], examID)
		if e != nil {
			return e
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return errors.New("no lockable attempt found")
		}
		return nil
	default:
		return errors.New("unknown admin command")
	}
}
func importBank(ctx context.Context, db *sql.DB, examID, path string) error {
	raw, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	if len(raw) > 20<<20 {
		return errors.New("bank exceeds 20 MiB")
	}
	var b bank
	if e = json.Unmarshal(raw, &b); e != nil {
		return e
	}
	if len(b.Questions) != 49 {
		return fmt.Errorf("expected 49 questions, got %d", len(b.Questions))
	}
	seen := map[string]bool{}
	counts := map[string]int{}
	type validated struct {
		item
		question
	}
	items := make([]validated, 0, 49)
	for _, v := range b.Questions {
		var q question
		var k key
		if e = json.Unmarshal(v.Public, &q); e != nil {
			return e
		}
		if e = json.Unmarshal(v.CorrectAnswer, &k); e != nil {
			return e
		}
		if q.ID == "" || q.SectionID != "reading-writing" && q.SectionID != "math" || q.Position < 1 || q.SectionID == "reading-writing" && q.Position > 27 || q.SectionID == "math" && q.Position > 22 {
			return errors.New("invalid question identity, section, or position")
		}
		slot := fmt.Sprintf("%s:%d", q.SectionID, q.Position)
		if seen[q.ID] || seen[slot] {
			return fmt.Errorf("duplicate question %s", slot)
		}
		seen[q.ID] = true
		seen[slot] = true
		counts[q.SectionID]++
		var public map[string]json.RawMessage
		if e = json.Unmarshal(v.Public, &public); e != nil {
			return e
		}
		if _, yes := public["correctAnswer"]; yes {
			return errors.New("public question contains answer key")
		}
		if _, yes := public["explanation"]; yes {
			return errors.New("public question contains explanation")
		}
		var publicTree any
		if e = json.Unmarshal(v.Public, &publicTree); e != nil {
			return e
		}
		if e = validateMedia(publicTree); e != nil {
			return e
		}
		if q.Kind == "multiple-choice" {
			if k.Kind != "choice" || len(q.Choices) < 2 {
				return fmt.Errorf("invalid choice key for %s", q.ID)
			}
			found := false
			for _, c := range q.Choices {
				if c.ID == k.ChoiceID {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("answer key does not match choices for %s", q.ID)
			}
		} else if q.Kind == "numeric" {
			if q.SectionID != "math" || k.Kind != "numeric" || strings.TrimSpace(k.Value) == "" {
				return fmt.Errorf("invalid numeric key for %s", q.ID)
			}
		} else {
			return fmt.Errorf("unknown question type for %s", q.ID)
		}
		if len(v.Explanation) == 0 {
			v.Explanation = json.RawMessage(`[]`)
		}
		items = append(items, validated{v, q})
	}
	if counts["reading-writing"] != 27 || counts["math"] != 22 {
		return errors.New("bank must have 27 Reading and Writing and 22 Math questions")
	}
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var attempts int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM attempts WHERE exam_id=$1`, examID).Scan(&attempts); e != nil {
		return e
	}
	if attempts > 0 {
		return errors.New("bank cannot change after attempts exist")
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM questions WHERE exam_id=$1`, examID); e != nil {
		return e
	}
	for _, v := range items {
		if _, e = tx.ExecContext(ctx, `INSERT INTO questions(id,exam_id,section_id,position,public_json,correct_answer,explanation) VALUES($1,$2,$3,$4,$5,$6,$7)`, v.ID, examID, v.SectionID, v.Position, v.Public, v.CorrectAnswer, v.Explanation); e != nil {
			return e
		}
	}
	return tx.Commit()
}
