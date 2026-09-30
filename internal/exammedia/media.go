package exammedia

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"strings"
)

// Asset is the portable JSON representation. PostgreSQL stores decoded bytes.
type Asset struct {
	ID         string `json:"id"`
	MimeType   string `json:"mimeType"`
	DataBase64 string `json:"dataBase64"`
}
type StoredAsset struct {
	ID, MimeType string
	Data         []byte
}

func Decode(a Asset) (StoredAsset, error) {
	if a.ID == "" || len(a.ID) > 160 {
		return StoredAsset{}, fmt.Errorf("invalid asset id")
	}
	data, err := base64.StdEncoding.DecodeString(a.DataBase64)
	if err != nil || len(data) == 0 || len(data) > 10<<20 {
		return StoredAsset{}, fmt.Errorf("asset %s must contain valid base64, at most 10 MiB", a.ID)
	}
	if a.MimeType != "image/png" && a.MimeType != "image/jpeg" && a.MimeType != "image/svg+xml" {
		return StoredAsset{}, fmt.Errorf("unsupported asset MIME type %s", a.MimeType)
	}
	if a.MimeType != "image/svg+xml" {
		config, format, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || "image/"+format != a.MimeType || config.Width > 16000 || config.Height > 16000 {
			return StoredAsset{}, fmt.Errorf("asset %s is not a valid %s image", a.ID, a.MimeType)
		}
	}
	return StoredAsset{a.ID, a.MimeType, data}, nil
}

// Normalize replaces inline images with references and validates all asset references.
// Graph/table blocks stay as structured JSON; scanned figures use image assets.
func Normalize(raw json.RawMessage, assets map[string]StoredAsset) (json.RawMessage, map[string]StoredAsset, error) {
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return nil, nil, err
	}
	used := map[string]StoredAsset{}
	err := walk(tree, func(block map[string]any) error {
		url, _ := block["url"].(string)
		if strings.HasPrefix(url, "asset:") {
			id := strings.TrimPrefix(url, "asset:")
			a, ok := assets[id]
			if !ok {
				return fmt.Errorf("missing image asset %s", id)
			}
			used[id] = a
			return nil
		}
		if !strings.HasPrefix(url, "data:image/") {
			return fmt.Errorf("images must use asset references or embedded data URIs")
		}
		header, data, ok := strings.Cut(url, ",")
		if !ok || !strings.HasSuffix(header, ";base64") {
			return fmt.Errorf("image must use a base64 data URI")
		}
		mime := strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
		sum := sha256.Sum256([]byte(url))
		id := hex.EncodeToString(sum[:])
		a, err := Decode(Asset{id, mime, data})
		if err != nil {
			return err
		}
		used[id] = a
		block["url"] = "asset:" + id
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	result, err := json.Marshal(tree)
	return result, used, err
}

// Hydrate is called only after the API has authorized the section or released review.
func Hydrate(raw json.RawMessage, assets map[string]StoredAsset) (json.RawMessage, error) {
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return nil, err
	}
	err := walk(tree, func(block map[string]any) error {
		url, _ := block["url"].(string)
		if !strings.HasPrefix(url, "asset:") {
			return nil
		}
		id := strings.TrimPrefix(url, "asset:")
		a, ok := assets[id]
		if !ok {
			return fmt.Errorf("stored image asset %s is missing", id)
		}
		block["url"] = "data:" + a.MimeType + ";base64," + base64.StdEncoding.EncodeToString(a.Data)
		return nil
	})
	if err != nil {
		return nil, err
	}
	result, err := json.Marshal(tree)
	return result, err
}
func walk(v any, visit func(map[string]any) error) error {
	switch x := v.(type) {
	case map[string]any:
		if x["kind"] == "image" {
			if err := visit(x); err != nil {
				return err
			}
		}
		for _, child := range x {
			if err := walk(child, visit); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range x {
			if err := walk(child, visit); err != nil {
				return err
			}
		}
	}
	return nil
}
