package publicationmedia

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func Directory() string {
	if path := os.Getenv("ADMIN_MEDIA_DIR"); path != "" {
		return path
	}
	return "data/admin-media"
}
func ServeFile(w http.ResponseWriter, r *http.Request, path string) {
	// Bot and backend share a media volume. Symlinks and paths outside it are rejected.
	roots := []string{Directory(), os.Getenv("BOT_MEDIA_DIR")}
	if roots[1] == "" {
		roots[1] = "/app/data/media"
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		notFound(w)
		return
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		notFound(w)
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
	notFound(w)
}

func notFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(404)
	_, _ = w.Write([]byte("{\"error\":\"not_found\"}"))
}
