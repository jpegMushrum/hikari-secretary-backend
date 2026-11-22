package adminapi

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/domain"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/publicationmedia"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var mediaIDPattern = regexp.MustCompile(`^[0-9a-f]{32}\.(jpg|png)$`)

func (h *Management) uploadMedia(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 10<<20))
	if err != nil {
		writeError(w, 413, "media_too_large")
		return
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width+cfg.Height > 10000 || cfg.Width > 20*cfg.Height || cfg.Height > 20*cfg.Width {
		writeError(w, 400, "invalid_image")
		return
	}
	if _, _, err = image.Decode(bytes.NewReader(data)); err != nil {
		writeError(w, 400, "invalid_image")
		return
	}
	suffix := ".jpg"
	if format == "png" {
		suffix = ".png"
	} else if format != "jpeg" {
		writeError(w, 400, "invalid_image")
		return
	}
	key := make([]byte, 16)
	if _, err = rand.Read(key); err != nil {
		managementError(w, err)
		return
	}
	id := hex.EncodeToString(key) + suffix
	if err = os.MkdirAll(publicationmedia.Directory(), 0750); err != nil {
		managementError(w, err)
		return
	}
	if err = os.WriteFile(filepath.Join(publicationmedia.Directory(), id), data, 0640); err != nil {
		managementError(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"id": id, "size": len(data), "content_type": "image/" + format})
}
func (h *Management) getMedia(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !mediaIDPattern.MatchString(id) {
		writeError(w, 400, "invalid_request")
		return
	}
	w.Header().Set("Content-Disposition", "inline")
	http.ServeFile(w, r, filepath.Join(publicationmedia.Directory(), id))
}
func configuredTargets() ([]domain.Target, error) {
	path := os.Getenv("TARGETS_PATH")
	if path == "" {
		path = "targets.json"
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var config struct {
		Telegram []struct {
			Key    string
			Name   string
			Chat   json.RawMessage `json:"chat_id"`
			Thread *int64          `json:"message_thread_id"`
		} `json:"telegram"`
	}
	if err = json.Unmarshal(raw, &config); err != nil {
		return nil, err
	}
	targets := []domain.Target{}
	for _, t := range config.Telegram {
		dest := strings.Trim(string(t.Chat), "\"")
		if t.Key == "" || t.Name == "" || dest == "" || dest == "null" {
			return nil, fmt.Errorf("invalid target")
		}
		targets = append(targets, domain.Target{Key: t.Key, Name: t.Name, Destination: dest, MessageThreadID: t.Thread})
	}
	return targets, nil
}
func (h *Management) targets(w http.ResponseWriter, r *http.Request) {
	ts, err := configuredTargets()
	if err != nil {
		managementError(w, err)
		return
	}
	items := []map[string]any{}
	for _, t := range ts {
		items = append(items, map[string]any{"key": t.Key, "name": t.Name})
	}
	writeJSON(w, 200, items)
}
func (h *Management) createPublication(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text       string   `json:"text"`
		MediaIDs   []string `json:"media_ids"`
		TargetKeys []string `json:"target_keys"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if len(body.MediaIDs) > 10 || len([]rune(body.Text)) > 4096 || (len(body.MediaIDs) > 0 && len([]rune(body.Text)) > 1024) || (strings.TrimSpace(body.Text) == "" && len(body.MediaIDs) == 0) {
		writeError(w, 400, "invalid_request")
		return
	}
	draft := domain.Draft{CreatorTelegramID: administratorID(r), Text: body.Text, Source: "miniapp"}
	if key := r.Header.Get("Idempotency-Key"); key != "" {
		if len(key) > 128 || strings.TrimSpace(key) != key {
			writeError(w, 400, "invalid_request")
			return
		}
		draft.SourceKey = fmt.Sprintf("miniapp:%d:%s", draft.CreatorTelegramID, key)
	}
	if r.Method == "PUT" {
		id, ok := pathID(r)
		if !ok {
			writeError(w, 400, "invalid_request")
			return
		}
		p, found, err := h.Publications.Publication(r.Context(), id)
		if err != nil {
			managementError(w, err)
			return
		}
		if !found {
			writeError(w, 404, "not_found")
			return
		}
		if p["status"] != "draft" {
			writeError(w, 409, "invalid_state")
			return
		}
		draft.ID = id
	}
	for i, id := range body.MediaIDs {
		if !mediaIDPattern.MatchString(id) {
			writeError(w, 400, "invalid_media")
			return
		}
		path := filepath.Join(publicationmedia.Directory(), id)
		if _, err := os.Stat(path); err != nil {
			writeError(w, 400, "invalid_media")
			return
		}
		draft.Media = append(draft.Media, domain.Media{Position: i, Path: path})
	}
	available, err := configuredTargets()
	if err != nil {
		managementError(w, err)
		return
	}
	targets := []domain.Target{}
	seen := map[string]bool{}
	for _, key := range body.TargetKeys {
		found := false
		for _, target := range available {
			if target.Key == key && !seen[key] {
				targets = append(targets, target)
				seen[key] = true
				found = true
				break
			}
		}
		if !found {
			writeError(w, 400, "invalid_target")
			return
		}
	}
	draft.Targets = targets
	id, err := h.Publications.CreateDraft(r.Context(), draft)
	if err != nil {
		managementError(w, err)
		return
	}
	status := 201
	if draft.ID > 0 {
		status = 200
	}
	writeJSON(w, status, map[string]any{"id": id})
}
