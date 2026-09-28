# Question content format

The organizer imports questions through `go run ./cmd/admin import private/exam-bank.json`. The bank contains 27 Reading and Writing items and 22 Math items. Keep `correctAnswer` and `explanation` outside the `public` object. The frontend displays the public content; the Go service grades the private key.

For Reading and Writing, put source material in `passages` and the task stem in `prompt`. This keeps the passage on the left and the question and answer choices on the right. Existing imported questions with only `prompt` are displayed with their source text on the left as a compatibility fallback; new questions should use the explicit format.

```json
{
  "id": "reading-writing-1",
  "sectionId": "reading-writing",
  "position": 1,
  "kind": "multiple-choice",
  "readingSkill": "inference",
  "passages": [
    {
      "id": "source",
      "content": [
        { "kind": "text", "text": "The garden reopened after a long winter. Visitors noticed that the early flowers appeared beside the shaded path.", "marks": [{ "start": 4, "end": 10, "style": "underline" }] }
      ]
    }
  ],
  "prompt": [{ "kind": "text", "text": "Which choice is best supported by the passage?" }],
  "choices": [
    { "id": "A", "content": [{ "kind": "text", "text": "An original sample choice." }] },
    { "id": "B", "content": [{ "kind": "text", "text": "Another original sample choice." }] },
    { "id": "C", "content": [{ "kind": "text", "text": "A third original sample choice." }] },
    { "id": "D", "content": [{ "kind": "text", "text": "A fourth original sample choice." }] }
  ]
}
```

`marks` offsets count JavaScript string characters starting at zero. Valid styles are `underline`, `italic`, and `bold`. Reading skill values are `words-in-context`, `inference`, `cross-text-connections`, `rhetorical-synthesis`, `standard-english-conventions`, `transitions`, and `text-structure`. The skill is optional metadata; the question writer supplies the actual task text and key.

Additional blocks can appear inside any passage or prompt:

```json
{ "kind": "list", "items": ["An original note.", "Another note."], "ordered": false }
{ "kind": "table", "caption": "Sample observations", "headers": ["Group", "Count"], "rows": [["A", "12"], ["B", "15"]] }
{ "kind": "graph", "title": "Sample trend", "xLabel": "Time", "yLabel": "Measure", "xMin": 0, "xMax": 10, "yMin": 0, "yMax": 10, "points": [{ "x": 2, "y": 7 }, { "x": 8, "y": 3 }], "lines": [{ "from": { "x": 0, "y": 9 }, "to": { "x": 10, "y": 1 } }] }
```

`ordered` and `lines` are optional. Math graphs and images have zoom, reset, enlarge, and pan controls. An `image` block can also carry a graph supplied as a base64 PNG, JPEG, or SVG data URL; include meaningful `alt` text. The importer refuses question images loaded from external URLs so unreleased exam media stays gated by the authenticated API.
