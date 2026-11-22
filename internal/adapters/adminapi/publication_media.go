package adminapi

import (
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/publicationmedia"
	"net/http"

	"strconv"
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
	publicationmedia.ServeFile(w, r, path)
}
