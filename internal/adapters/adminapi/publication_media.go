package adminapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Media is addressed by publication and ordinal, never by client-supplied path.
func (h *Management) publicationMedia(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	position, err := strconv.Atoi(r.PathValue("position"))
	if !ok || err != nil || position < 0 {
		writeError(w, 400, "invalid_request")
		return
	}
	var path string
	err = h.Pool.QueryRow(r.Context(), "SELECT path FROM publication_media WHERE publication_id=$1 ORDER BY position OFFSET $2 LIMIT 1", id, position).Scan(&path)
	if err != nil {
		managementError(w, err)
		return
	}
	// Bot and backend share a media volume. Symlinks and paths outside it are rejected.
	roots := []string{mediaDirectory(), os.Getenv("BOT_MEDIA_DIR")}
	if roots[1] == "" {
		roots[1] = "/app/data/media"
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		writeError(w, 404, "not_found")
		return
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		writeError(w, 404, "not_found")
		return
	}
	for _, root := range roots {
		root, err = filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		root, err = filepath.Abs(root)
		if err != nil {
			continue
		}
		rel, e := filepath.Rel(root, resolved)
		if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			continue
		}
		ext := strings.ToLower(filepath.Ext(resolved))
		if ext != ".jpg" && ext != ".jpeg" && ext != ".png" {
			break
		}
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeFile(w, r, resolved)
		return
	}
	writeError(w, 404, "not_found")
}
