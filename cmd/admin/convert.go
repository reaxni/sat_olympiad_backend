package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type sourceBank struct {
	TestTitle      string `json:"test_title"`
	TotalQuestions int    `json:"total_questions"`
	Questions      []struct {
		ID       int      `json:"id"`
		Question string   `json:"question"`
		Options  []string `json:"options"`
		Answer   string   `json:"answer"`
		Points   int      `json:"points"`
	} `json:"questions"`
}

// convertSources builds the backend's private bank. Its public question object
// contains no key; the importer stores keys separately from what the API serves.
func convertSources(readingPath, mathPath, outputPath string) error {
	result := bank{Questions: []item{}}
	for _, source := range []struct {
		path, section string
		count         int
	}{{readingPath, "reading-writing", 27}, {mathPath, "math", 22}} {
		raw, err := os.ReadFile(source.path)
		if err != nil {
			return err
		}
		var input sourceBank
		if err := json.Unmarshal(raw, &input); err != nil {
			return fmt.Errorf("%s: %w", source.path, err)
		}
		if input.TotalQuestions != source.count || len(input.Questions) != source.count {
			return fmt.Errorf("%s: expected %d questions", source.path, source.count)
		}
		seen := map[int]bool{}
		for _, q := range input.Questions {
			if q.ID < 1 || q.ID > source.count || seen[q.ID] || strings.TrimSpace(q.Question) == "" {
				return fmt.Errorf("%s: invalid or repeated question %d", source.path, q.ID)
			}
			seen[q.ID] = true
			// The source for Math #20 refers to missing survey data. Supply
			// the two percentages so the generated item is answerable.
			if source.section == "math" && q.ID == 20 {
				q.Question = "In a survey, 47% of employees with 10 years of experience or less preferred working on-site, while 62% of employees with more than 10 years of experience preferred working on-site. What is the difference between these percentages?"
			}
			id := fmt.Sprintf("%s-%d", source.section, q.ID)
			public := map[string]any{"id": id, "sectionId": source.section, "position": q.ID, "prompt": []any{map[string]string{"kind": "text", "text": q.Question}}}
			if source.section == "reading-writing" {
				task := "Which choice best answers the question based on the text?"
				skill := ""
				switch {
				case strings.Contains(q.Question, "Text 1:") && strings.Contains(q.Question, "Text 2:"):
					task, skill = "Which choice best describes the relationship between the texts?", "cross-text-connections"
				case strings.Contains(q.Question, "____"):
					task = "Which choice best completes the text?"
				case strings.Contains(strings.ToLower(q.Question), "following notes"):
					task, skill = "Which choice best uses the notes to meet the goal?", "rhetorical-synthesis"
				}
				public["passages"] = []any{map[string]any{"id": "source", "content": []any{map[string]string{"kind": "text", "text": q.Question}}}}
				public["prompt"] = []any{map[string]string{"kind": "text", "text": task}}
				if skill != "" {
					public["readingSkill"] = skill
				}
			}
			var correct any
			if q.Options == nil {
				if source.section != "math" || strings.TrimSpace(q.Answer) == "" {
					return fmt.Errorf("%s: invalid numeric answer for %s", source.path, id)
				}
				public["kind"] = "numeric"
				public["studentProducedResponseDirections"] = true
				correct = map[string]string{"kind": "numeric", "value": strings.TrimSpace(q.Answer)}
			} else {
				if len(q.Options) != 4 {
					return fmt.Errorf("%s: expected four options for %s", source.path, id)
				}
				choices := make([]any, 0, 4)
				for i, option := range q.Options {
					letter := string(rune('A' + i))
					prefix := letter + ") "
					if !strings.HasPrefix(option, prefix) {
						return fmt.Errorf("%s: option %s missing label for %s", source.path, letter, id)
					}
					choices = append(choices, map[string]any{"id": letter, "content": []any{map[string]string{"kind": "text", "text": strings.TrimPrefix(option, prefix)}}})
				}
				// Supplied Math #16 says C, but equal GH/LM, equal GF/LK
				// (by the given ratio), and the included 30-degree angle prove
				// congruence by SAS without another fact: choice D.
				if source.section == "math" && q.ID == 16 {
					q.Answer = "D"
				}
				if !strings.Contains("ABCD", q.Answer) || len(q.Answer) != 1 {
					return fmt.Errorf("%s: invalid choice key for %s", source.path, id)
				}
				public["kind"] = "multiple-choice"
				public["choices"] = choices
				correct = map[string]string{"kind": "choice", "choiceId": q.Answer}
			}
			p, err := json.Marshal(public)
			if err != nil {
				return err
			}
			k, err := json.Marshal(correct)
			if err != nil {
				return err
			}
			result.Questions = append(result.Questions, item{Public: p, CorrectAnswer: k, Explanation: json.RawMessage(`[]`), Difficulty: "hard"})
		}
	}
	if len(result.Questions) != 49 {
		return errors.New("incomplete exam")
	}
	output, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0700); err != nil {
		return err
	}
	return os.WriteFile(outputPath, append(output, '\n'), 0600)
}
