package botapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWriteJSONWritesCollectionBody(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeJSON(recorder, http.StatusOK, []map[string]any{{"post_id": int64(1), "starts_at": time.Now().UTC()}})
	if recorder.Code != http.StatusOK || recorder.Body.Len() == 0 {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}
