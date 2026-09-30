package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	_ "github.com/jackc/pgx/v5/stdlib"
	"os"
	"satbackend/internal/exammedia"
	"satbackend/internal/localenv"
	"strings"
	"time"
)

type item struct {
	Public        json.RawMessage `json:"public"`
	CorrectAnswer json.RawMessage `json:"correctAnswer"`
	Explanation   json.RawMessage `json:"explanation"`
	Difficulty    string          `json:"difficulty"`
	Source        json.RawMessage `json:"source,omitempty"`
}
type bank struct {
	Questions []item            `json:"questions"`
	Assets    []exammedia.Asset `json:"assets,omitempty"`
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

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	if err := localenv.Load(".env"); err != nil {
		return err
	}
	if len(os.Args) < 3 {
		return errors.New("usage: admin convert <reading.json> <math.json> <output.json> | validate <bank.json> | import <bank.json> [exam-id] | release <explanations|leaderboard> | lock <attempt-id> <reason>")
	}
	if os.Args[1] == "convert" {
		if len(os.Args) != 5 {
			return errors.New("usage: admin convert <reading.json> <math.json> <output.json>")
		}
		return convertSources(os.Args[2], os.Args[3], os.Args[4])
	}
	if os.Args[1] == "validate" {
		if e := importBank(context.Background(), nil, "", os.Args[2]); e != nil {
			return e
		}
		fmt.Println("Validated 27 Reading and Writing and 22 Math questions, keys, and image assets.")
		return nil
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if e = db.PingContext(ctx); e != nil {
		return e
	}
	if os.Args[1] == "import" {
		fmt.Println("Connected to PostgreSQL; validating and importing the bank.")
	}
	examID := os.Getenv("EXAM_ID")
	if examID == "" {
		examID = "1609-olympiad"
	}
	switch os.Args[1] {
	case "import":
		if len(os.Args) > 3 {
			examID = os.Args[3]
		}
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
	if len(raw) > 100<<20 {
		return errors.New("bank exceeds 100 MiB")
	}
	var b bank
	if e = json.Unmarshal(raw, &b); e != nil {
		return e
	}
	if len(b.Questions) != 49 {
		return fmt.Errorf("expected 49 questions, got %d", len(b.Questions))
	}
	seen := map[string]bool{}
	assets := map[string]exammedia.StoredAsset{}
	for _, asset := range b.Assets {
		if _, exists := assets[asset.ID]; exists {
			return fmt.Errorf("duplicate asset %s", asset.ID)
		}
		decoded, err := exammedia.Decode(asset)
		if err != nil {
			return err
		}
		assets[asset.ID] = decoded
	}
	counts := map[string]int{}
	type validated struct {
		item
		question
		assets map[string]exammedia.StoredAsset
	}
	items := make([]validated, 0, 49)
	for _, v := range b.Questions {
		if v.Difficulty != "easy" && v.Difficulty != "medium" && v.Difficulty != "hard" {
			return errors.New("every question needs difficulty: easy, medium, or hard")
		}
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
		if len(v.Source) == 0 {
			v.Source = json.RawMessage(`{}`)
		}
		publicJSON, used, e := exammedia.Normalize(v.Public, assets)
		if e != nil {
			return fmt.Errorf("%s: %w", q.ID, e)
		}
		explanationJSON, explanationAssets, e := exammedia.Normalize(v.Explanation, assets)
		if e != nil {
			return fmt.Errorf("%s explanation: %w", q.ID, e)
		}
		for id, a := range explanationAssets {
			used[id] = a
		}
		v.Public = publicJSON
		v.Explanation = explanationJSON
		items = append(items, validated{v, q, used})
	}
	if counts["reading-writing"] != 27 || counts["math"] != 22 {
		return errors.New("bank must have 27 Reading and Writing and 22 Math questions")
	}
	if db == nil {
		return nil
	} // Offline validation uses the same checks as import.
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	migration, e := os.ReadFile("migrations/004_question_assets.sql")
	if e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, string(migration)); e != nil {
		return e
	}
	// Lock the exam row against concurrent starts/imports. New exams require a
	// schedule from the organizer's environment and do not change the active API exam.
	var exists bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM exams WHERE id=$1)`, examID).Scan(&exists); e != nil {
		return e
	}
	if !exists {
		open, e := time.Parse(time.RFC3339, os.Getenv("EXAM_OPEN_AT"))
		if e != nil {
			return errors.New("EXAM_OPEN_AT is required to create a new exam")
		}
		close, e := time.Parse(time.RFC3339, os.Getenv("EXAM_ENTRY_CLOSE_AT"))
		if e != nil || !close.After(open) {
			return errors.New("EXAM_ENTRY_CLOSE_AT must be after EXAM_OPEN_AT")
		}
		title := os.Getenv("EXAM_TITLE")
		if title == "" {
			title = examID
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO exams(id,title,opens_at,entry_closes_at) VALUES($1,$2,$3,$4) ON CONFLICT(id) DO NOTHING`, examID, title, open, close); e != nil {
			return e
		}
	}
	var lockedID string
	if e = tx.QueryRowContext(ctx, `SELECT id FROM exams WHERE id=$1 FOR UPDATE`, examID).Scan(&lockedID); e != nil {
		return e
	}
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
		if _, e = tx.ExecContext(ctx, `INSERT INTO questions(id,exam_id,section_id,position,public_json,correct_answer,explanation,difficulty,source_json) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, v.ID, examID, v.SectionID, v.Position, v.Public, v.CorrectAnswer, v.Explanation, v.Difficulty, v.Source); e != nil {
			return e
		}
		for _, asset := range v.assets {
			if _, e = tx.ExecContext(ctx, `INSERT INTO question_assets(question_id,id,mime_type,data) VALUES($1,$2,$3,$4)`, v.ID, asset.ID, asset.MimeType, asset.Data); e != nil {
				return e
			}
		}
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	assetCount, assetBytes := 0, 0
	for _, v := range items {
		for _, a := range v.assets {
			assetCount++
			assetBytes += len(a.Data)
		}
	}
	fmt.Printf("Imported %d questions and %d image assets (%d bytes) for exam %s.\n", len(items), assetCount, assetBytes, examID)
	return nil
}
