package app

import (
	"encoding/json"
	"testing"
)

// The history list renders only summary fields. It must not carry the full
// per-record resource refs (both inventories), which bloat the response, while
// the stored record must keep them for restart scope reconstruction.
func TestHistoryListOmitsResourcesButStoreKeepsThem(t *testing.T) {
	h := setup(t)
	v := h.plan()
	h.call("POST", "/api/plans/"+v.ID+"/deploy", map[string]any{"digest": v.Review.Digest, "confirm": true}, nil, 200)

	var listed []map[string]any
	h.call("GET", "/api/history", nil, &listed, 200)
	if len(listed) == 0 {
		t.Fatal("history list is empty after a deployment")
	}
	for _, rec := range listed {
		if res, ok := rec["resources"]; ok && res != nil {
			if arr, isArr := res.([]any); !isArr || len(arr) > 0 {
				t.Fatalf("history list carried resource refs it never renders: %v", res)
			}
		}
	}

	stored, err := h.app.store.GetRecord(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Resources) == 0 {
		t.Fatal("stored record lost its resource refs; restart scope reconstruction needs them")
	}
}

// A plan is polled every ~2s while it runs. Polling a record whose native
// status has not changed must not rewrite the records file (it holds s.mu and
// fsyncs). Observed via the record's UpdatedAt, which the save would advance.
func TestPollingUnchangedStatusDoesNotResaveRecord(t *testing.T) {
	h := setup(t)
	v := h.plan()
	h.call("POST", "/api/plans/"+v.ID+"/deploy", map[string]any{"digest": v.Review.Digest, "confirm": true}, nil, 200)

	before, err := h.app.store.GetRecord(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The native fixture keeps returning COMPLETED, so these polls see no change.
	for i := 0; i < 3; i++ {
		var out json.RawMessage
		h.call("GET", "/api/plans/"+v.ID, nil, &out, 200)
	}
	after, err := h.app.store.GetRecord(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.UpdatedAt != before.UpdatedAt {
		t.Fatalf("unchanged-status poll rewrote the record: UpdatedAt %q -> %q", before.UpdatedAt, after.UpdatedAt)
	}
}
