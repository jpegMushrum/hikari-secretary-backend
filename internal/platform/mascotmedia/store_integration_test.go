//go:build integration

package mascotmedia

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"image"
	"image/png"
	"os"
	"testing"
)

// MASCOT_MEDIA_TEST_ENDPOINT points to the disposable S3-compatible local service.
func TestLocalObjectStorageRoundTrip(t *testing.T) {
	endpoint := os.Getenv("MASCOT_MEDIA_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("MASCOT_MEDIA_TEST_ENDPOINT is not configured")
	}
	t.Setenv("APP_ENV", "development")
	t.Setenv("MASCOT_S3_ENDPOINT", endpoint)
	t.Setenv("MASCOT_S3_BUCKET", "hikari-local")
	t.Setenv("MASCOT_S3_PREFIX", "media/mascot-tests")
	t.Setenv("MASCOT_S3_REGION", "us-east-1")
	t.Setenv("MASCOT_S3_ACCESS_KEY_ID", "local-mascot")
	t.Setenv("MASCOT_S3_SECRET_ACCESS_KEY", "local-only")
	store, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	rawKey := make([]byte, 16)
	rand.Read(rawKey)
	key := hex.EncodeToString(rawKey)
	var data bytes.Buffer
	if err = png.Encode(&data, image.NewGray(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = store.Put(ctx, key, data.Bytes(), "image/png"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Delete(ctx, key); err != nil {
			t.Error(err)
		}
	})
	file, mime, err := store.Get(ctx, key)
	if err != nil || !bytes.Equal(data.Bytes(), file) || mime != "image/png" {
		t.Fatalf("readback mismatch, mime %q, err %v", mime, err)
	}
}
