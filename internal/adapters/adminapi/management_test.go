package adminapi

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestManagementRejectsInvalidRequestsBeforeDatabase(t *testing.T) {
	router := (&Management{}).Router(NewRouter(&eventsStub{}))
	for _, test := range []struct{ method, path, body string }{
		{"POST", "/publications", "{}"},
		{"POST", "/publications", `{"text":"x","creator_telegram_id":999}`},
		{"POST", "/publications", `{"text":"x"} {}`},
		{"POST", "/publications", `{"text":"x","media_ids":["../secret"]}`},
		{"POST", "/media", "not an image"},
		{"GET", "/media/not-an-id", ""},
		{"GET", "/publications?sort=drop_table", ""},
		{"GET", "/feedback?event_id=-1", ""},
		{"GET", "/feedback?search=a&search=b", ""},
		{"GET", "/feedback?kind=unknown", ""},
		{"GET", "/publications?status=draft&status=sent", ""},
	} {
		t.Run(test.method+test.path+test.body, func(t *testing.T) {
			res := httptest.NewRecorder()
			router.ServeHTTP(res, httptest.NewRequest(test.method, "/api/v1/admin"+test.path, strings.NewReader(test.body)))
			if res.Code != 400 {
				t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
			}
		})
	}
}
