package server

import (
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
	if strikes["remaining"] != 0 || strikes["disqualified"] != false {
		t.Fatal("observations must not automatically disqualify")
	}
	p := data["progress"].(map[string]any)
	if p["sectionId"] != "math" || p["deadlineAt"] == nil {
		t.Fatal("math progress missing")
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
