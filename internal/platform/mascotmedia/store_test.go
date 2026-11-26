package mascotmedia

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStoreSignsRequestsAndKeepsObjectsInsidePrivatePrefix(t *testing.T) {
	var original bytes.Buffer
	if err := png.Encode(&original, image.NewGray(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	var saved []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/shared-bucket/media/mascot/card_1" || !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Errorf("unexpected object path or unsigned request: %s", r.URL.Path)
			http.Error(w, "forbidden", 403)
			return
		}
		switch r.Method {
		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil || r.Header.Get("Content-Type") != "image/png" || !bytes.Equal(body, original.Bytes()) {
				t.Errorf("invalid PUT: %v", err)
			}
			saved = body
		case http.MethodGet:
			w.Header().Set("Content-Length", fmt.Sprint(len(saved)))
			_, _ = w.Write(saved)
		case http.MethodDelete:
			saved = nil
			w.WriteHeader(204)
		}
	}))
	defer server.Close()
	t.Setenv("APP_ENV", "development")
	t.Setenv("MASCOT_S3_ENDPOINT", server.URL)
	t.Setenv("MASCOT_S3_BUCKET", "shared-bucket")
	t.Setenv("MASCOT_S3_PREFIX", "media/mascot")
	t.Setenv("MASCOT_S3_REGION", "ru-central1")
	t.Setenv("MASCOT_S3_ACCESS_KEY_ID", "unit-key")
	t.Setenv("MASCOT_S3_SECRET_ACCESS_KEY", "unit-secret")
	s, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = s.Put(ctx, "card_1", original.Bytes(), "image/png"); err != nil {
		t.Fatal(err)
	}
	data, mime, err := s.Get(ctx, "card_1")
	if err != nil || mime != "image/png" || !bytes.Equal(data, original.Bytes()) {
		t.Fatalf("unexpected readback: %s %v", mime, err)
	}
	if err = s.Delete(ctx, "card_1"); err != nil || saved != nil {
		t.Fatalf("unexpected deletion: %v", err)
	}
	if err = s.Delete(ctx, "../backups/object"); err == nil {
		t.Fatal("accepted non-opaque key")
	}
	t.Setenv("APP_ENV", "production")
	if _, err = FromEnv(); err == nil {
		t.Error("accepted plain HTTP in production")
	}
}

func TestUnconfiguredBucketLeavesOtherFeaturesAvailable(t *testing.T) {
	t.Setenv("MASCOT_S3_BUCKET", "")
	if store, err := FromEnv(); store != nil || err != nil {
		t.Fatalf("%v %v", store, err)
	}
}
