package exammedia

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"strings"
	"testing"
)

func fixture(t *testing.T) Asset {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	return Asset{"figure", "image/png", base64.StdEncoding.EncodeToString(b.Bytes())}
}

func TestImageStorageRoundTrip(t *testing.T) {
	a := fixture(t)
	stored, err := Decode(a)
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"prompt":[{"kind":"image","url":"asset:figure","alt":"graph"}],"choices":[{"id":"A","content":[{"kind":"image","url":"asset:figure","alt":"choice"}]}]}`)
	ref, used, err := Normalize(raw, map[string]StoredAsset{a.ID: stored})
	if err != nil {
		t.Fatal(err)
	}
	if len(used) != 1 || strings.Contains(string(ref), a.DataBase64) {
		t.Fatal("database JSON must hold references, not image bytes")
	}
	hydrated, err := Hydrate(ref, used)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(hydrated), "data:image/png;base64,"+a.DataBase64) != 2 {
		t.Fatal("nested prompt and choice images were not restored")
	}
	if _, err := Hydrate(ref, nil); err == nil {
		t.Fatal("missing stored assets must fail, not silently remove a figure")
	}
}

func TestLegacyEmbeddedImagesAndStructuredGraphs(t *testing.T) {
	a := fixture(t)
	raw := json.RawMessage(`[{"kind":"image","url":"data:image/png;base64,` + a.DataBase64 + `","alt":"diagram"},{"kind":"graph","title":"Original graph","series":[{"points":[[0,0],[1,2]]}]}]`)
	ref, used, err := Normalize(raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(used) != 1 || !strings.Contains(string(ref), `"title":"Original graph"`) {
		t.Fatal("legacy images or graph data lost")
	}
	if _, err := Hydrate(ref, used); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidMediaRejected(t *testing.T) {
	a := fixture(t)
	a.MimeType = "image/jpeg"
	if _, err := Decode(a); err == nil {
		t.Fatal("forged MIME type accepted")
	}
	for _, url := range []string{"asset:missing", "https://example.com/public-answer.png", "data:image/png;base64,invalid"} {
		raw := json.RawMessage(`[{"kind":"image","url":"` + url + `"}]`)
		if _, _, err := Normalize(raw, nil); err == nil {
			t.Fatalf("invalid image accepted: %s", url)
		}
	}
}
