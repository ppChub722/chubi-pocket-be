package transactions

import (
	"encoding/json"
	"testing"
)

// TestSplitEditContactPresence — "contact_id": null (drop the contact) is
// told apart from a body without the key (unchanged).
func TestSplitEditContactPresence(t *testing.T) {
	var req EditSplitsRequest
	body := `{"splits":[
		{"debt_id":"0190e5a0-0000-7000-8000-000000000001","owed_amount":5,"contact_id":null},
		{"debt_id":"0190e5a0-0000-7000-8000-000000000002","owed_amount":5},
		{"person_name":"New","contact_id":"0190e5a0-0000-7000-8000-000000000003","owed_amount":5}]}`
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	got := *req.Splits
	if !got[0].ContactIDSet || got[0].ContactID != nil {
		t.Errorf("explicit null: %+v", got[0])
	}
	if got[1].ContactIDSet {
		t.Errorf("no key: %+v", got[1])
	}
	if !got[2].ContactIDSet || got[2].ContactID == nil || got[2].OwedAmount != 5 || got[2].PersonName != "New" {
		t.Errorf("a contact: %+v", got[2])
	}
}
