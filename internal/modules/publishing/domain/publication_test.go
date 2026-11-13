package domain

import (
	"encoding/json"
	"testing"
)

func TestDeliveryUsesBotAPIJSONContract(t *testing.T) {
	payload, err := json.Marshal(Delivery{ID: 11, PublicationID: 21, CreatorTelegramID: 31, TargetKey: "channel", TargetName: "Channel", Destination: "-1001", Text: "hello", Entities: []map[string]any{}, MediaPaths: []string{}, Attempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(payload, &value); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "publication_id", "creator_telegram_id", "target_key", "media_paths", "event_enabled"} {
		if _, ok := value[key]; !ok {
			t.Fatalf("missing JSON key %q: %s", key, payload)
		}
	}
	if _, leaked := value["PublicationID"]; leaked {
		t.Fatalf("Go field names must not leak into API: %s", payload)
	}
}
