package server

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestScoringBoundaries(t *testing.T) {
	for _, c := range []struct{ right, total, want int }{{0, 27, 200}, {27, 27, 800}, {0, 22, 200}, {22, 22, 800}, {11, 22, 500}} {
		if got := scaled(c.right, c.total); got != c.want {
			t.Fatalf("scaled(%d,%d)=%d want %d", c.right, c.total, got, c.want)
		}
	}
}
func TestDifficultyWeightedScore(t *testing.T) {
	easy := difficultyWeight("easy")
	medium := difficultyWeight("medium")
	hard := difficultyWeight("hard")
	if easy != 1 || medium != 2 || hard != 3 {
		t.Fatal("difficulty weights changed")
	}
	total := easy + medium + hard
	if scaled(hard, total) <= scaled(easy, total) {
		t.Fatal("a correct hard question must contribute more than a correct easy question")
	}
	if scaled(0, total) != 200 || scaled(total, total) != 800 {
		t.Fatal("section scale must stay between 200 and 800")
	}
}
func TestNumericAndChoiceMatching(t *testing.T) {
	if !matches(&answerValue{Kind: "numeric", Value: "0.5"}, key{Kind: "numeric", Value: "1/2"}) {
		t.Fatal("equivalent fraction not accepted")
	}
	if matches(&answerValue{Kind: "numeric", Value: "0.51"}, key{Kind: "numeric", Value: "1/2"}) {
		t.Fatal("incorrect numeric value accepted")
	}
	if !matches(&answerValue{Kind: "choice", ChoiceID: "B"}, key{Kind: "choice", ChoiceID: "B"}) {
		t.Fatal("choice not accepted")
	}
	if matches(nil, key{Kind: "choice", ChoiceID: "B"}) {
		t.Fatal("unanswered question accepted")
	}
}
func TestAttemptProgressAndEventCount(t *testing.T) {
	now := time.Now().UTC()
	a := attemptRow{ID: "a", ExamID: "e", Phase: "in-progress", SectionID: "math", EventCount: 4}
	a.Started.Time = now
	a.Started.Valid = true
	a.Deadline.Time = now.Add(time.Minute)
	a.Deadline.Valid = true
	data := publicAttempt(a)
	strikes := data["strikes"].(map[string]any)
	if strikes["remaining"] != 1 || strikes["disqualified"] != false || strikes["limit"] != 5 {
		t.Fatal("four events must leave one remaining")
	}
	p := data["progress"].(map[string]any)
	if p["sectionId"] != "math" || p["deadlineAt"] == nil {
		t.Fatal("math progress missing")
	}
	a.EventCount = eventLimit
	a.Phase = "disqualified"
	a.Disqualified = sql.NullTime{Time: now, Valid: true}
	locked := publicAttempt(a)["strikes"].(map[string]any)
	if locked["remaining"] != 0 || locked["disqualified"] != true {
		t.Fatal("fifth event must leave no attempts remaining")
	}
}
func TestOriginAndMutationHeader(t *testing.T) {
	s := &Server{env: "production", origins: map[string]bool{"https://exam.example": true}}
	h := s.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	tests := []struct {
		origin, header string
		want           int
	}{{"https://exam.example", "1", 204}, {"https://other.example", "1", 403}, {"https://exam.example", "", 403}, {"", "1", 403}}
	for _, v := range tests {
		r := httptest.NewRequest("POST", "/x", nil)
		if v.origin != "" {
			r.Header.Set("Origin", v.origin)
		}
		if v.header != "" {
			r.Header.Set("X-Olympiad-Request", v.header)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != v.want {
			t.Errorf("origin=%q header=%q: %d want %d", v.origin, v.header, w.Code, v.want)
		}
	}
}

func TestPublicQuestionRejectsNestedAnswerKeys(t *testing.T) {
	if !publicQuestionSafe([]byte(`{"id":"q1","choices":[{"id":"A","content":[{"kind":"text","text":"An answer choice"}]}]}`)) {
		t.Fatal("a normal public question should be allowed")
	}
	for _, raw := range []string{
		`{"correctAnswer":{"choiceId":"A"}}`,
		`{"choices":[{"id":"A","isCorrect":true}]}`,
		`{"prompt":[{"answer_key":"A"}]}`,
	} {
		if publicQuestionSafe([]byte(raw)) {
			t.Fatalf("private key was exposed: %s", raw)
		}
	}
}
func TestAnswerToolsJSONShape(t *testing.T) {
	raw, e := json.Marshal(answerTools{Highlights: []highlight{{ID: "h", BlockID: "passage-1", Start: 1, End: 3, Text: "ab", Color: "yellow"}}})
	if e != nil {
		t.Fatal(e)
	}
	var v map[string]any
	if e = json.Unmarshal(raw, &v); e != nil {
		t.Fatal(e)
	}
	h := v["highlights"].([]any)[0].(map[string]any)
	if h["blockId"] != "passage-1" || h["start"] != float64(1) {
		t.Fatalf("wrong frontend annotation shape: %s", raw)
	}
}
