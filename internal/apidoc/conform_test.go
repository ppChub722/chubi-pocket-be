package apidoc

import (
	"strings"
	"testing"
)

func TestResponseConformance(t *testing.T) {
	badges := `{"unread_notifications":3,"pending_drafts":0}`
	counts := `{"categories":1,"tags":1,"contacts":1,"active_events":1,"open_debts":1,"budgets":1,"goals":{"count":1,"progress_pct":50.5},"debts_summary":{"owed_to_me":1,"i_owe":0,"net":1},"budgets_summary":{"total_budget":1,"total_spent":0,"utilization_pct":0,"over_limit_count":0},"scheduled":{"active_count":1,"upcoming_count":0}}`
	if err := Response("getMyCounts", 200, []byte(`{"badges":`+badges+`,"counts":`+counts+`}`)); err != nil {
		t.Fatalf("valid body rejected: %v", err)
	}
	cases := map[string]string{
		"extra key":   `{"badges":` + badges + `,"counts":` + counts + `,"oops":1}`,
		"missing key": `{"badges":` + badges + `}`,
		"wrong type":  `{"badges":{"unread_notifications":"3","pending_drafts":0},"counts":` + counts + `}`,
		"null":        `{"badges":null,"counts":null}`,
	}
	for name, body := range cases {
		if err := Response("getMyCounts", 200, []byte(body)); err == nil {
			t.Errorf("%s: should not conform", name)
		}
	}
	if err := Response("getMyCounts", 418, []byte(`{}`)); err == nil || !strings.Contains(err.Error(), "no 418") {
		t.Errorf("unknown status: %v", err)
	}
	if err := Response("nope", 200, []byte(`{}`)); err == nil {
		t.Errorf("unknown operationId should fail")
	}
}

func TestAllOfIsMerged(t *testing.T) {
	// AccountDetail = Account + summary / members_history: a bare {id} is not one.
	if err := Response("getAccount", 200, []byte(`{"id":"x"}`)); err == nil {
		t.Errorf("a bare {id} must not conform to AccountDetail")
	}
}
