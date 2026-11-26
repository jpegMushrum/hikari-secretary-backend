package mascotapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/mascot/domain"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/mascotmedia"
)

var errMediaUnavailable = errors.New("mascot image storage unavailable")
var errTooLarge = errors.New("mascot image exceeds upload limit")

// A 16 megapixel photo can take tens of MiB to decode. Keep uploads within
// the VPS memory budget while leaving reads and voting available.
var imageSlot = make(chan struct{}, 1)

func (h *Handler) upload(w http.ResponseWriter, r *http.Request, owner int64) {
	if h.Objects == nil {
		respond(w, nil, errMediaUnavailable)
		return
	}
	season, err := strconv.ParseInt(r.URL.Query().Get("season_id"), 10, 64)
	if err != nil || season <= 0 {
		respond(w, nil, domain.ErrInvalid)
		return
	}
	select {
	case imageSlot <- struct{}{}:
		defer func() { <-imageSlot }()
	case <-r.Context().Done():
		return
	}
	var token [16]byte
	rand.Read(token[:])
	key := hex.EncodeToString(token[:])
	if err = h.Repo.ReserveUpload(r.Context(), owner, season, key); err != nil {
		respond(w, nil, err)
		return
	}
	// Only objects that may have reached S3 require retryable deletion.
	putAttempted := false
	finished := false
	defer func() {
		if !finished {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
			defer cancel()
			if putAttempted {
				_ = h.Repo.DiscardUpload(ctx, owner, key)
			} else {
				_ = h.Repo.ReleaseReservation(ctx, owner, key)
			}
		}
	}()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, mascotmedia.MaxBytes))
	var limit *http.MaxBytesError
	if errors.As(err, &limit) {
		respond(w, nil, errTooLarge)
		return
	}
	if err != nil {
		respond(w, nil, domain.ErrInvalid)
		return
	}
	body, mime, err := mascotmedia.Prepare(body)
	if err != nil {
		respond(w, nil, domain.ErrInvalid)
		return
	}
	putAttempted = true
	if err = h.Objects.Put(r.Context(), key, body, mime); err != nil {
		respond(w, nil, errMediaUnavailable)
		return
	}
	if err = h.Repo.FinishUpload(r.Context(), owner, key); err != nil {
		respond(w, nil, err)
		return
	}
	finished = true
	respond(w, map[string]string{"key": key}, nil)
}

func (h *Handler) image(w http.ResponseWriter, r *http.Request, actor int64, admin bool) {
	key := r.PathValue("key")
	allowed, err := h.Repo.CanReadImage(r.Context(), actor, admin, key)
	if err != nil {
		respond(w, nil, err)
		return
	}
	if !allowed {
		respond(w, nil, domain.ErrNotFound)
		return
	}
	if h.Objects == nil {
		respond(w, nil, errMediaUnavailable)
		return
	}
	body, mime, err := h.Objects.Get(r.Context(), key)
	if err != nil {
		respond(w, nil, errMediaUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", mime)
	_, _ = w.Write(body)
}
