package pending

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
)

func ptr[T any](v T) *T { return &v }

func TestCreateRequestNeedsTheBasics(t *testing.T) {
	cases := []struct {
		name string
		d    Draft
		code string
	}{
		{"no type", Draft{Amount: ptr(10.0), Date: ptr("2026-10-08")}, "MISSING_TYPE"},
		{"no amount", Draft{Type: ptr("expense"), Date: ptr("2026-10-08")}, "MISSING_AMOUNT"},
		{"zero amount", Draft{Type: ptr("expense"), Amount: ptr(0.0), Date: ptr("2026-10-08")}, "MISSING_AMOUNT"},
		{"no date", Draft{Type: ptr("expense"), Amount: ptr(10.0)}, "MISSING_DATE"},
	}
	for _, c := range cases {
		_, err := createRequest(c.d)
		if got := toSubmitError(err).Code; got != c.code {
			t.Errorf("%s: code %q, want %q", c.name, got, c.code)
		}
	}
	req, err := createRequest(Draft{Type: ptr("income"), Amount: ptr(5.5), Date: ptr("2026-10-08")})
	if err != nil || req.Type != transactions.TypeIncome || req.Amount != 5.5 || req.AccountID != nil {
		t.Fatalf("complete draft: %+v, %v", req, err)
	}
}

func TestCheckDraftOnlyRejectsMalformed(t *testing.T) {
	if err := checkDraft(Draft{}); err != nil {
		t.Errorf("empty draft should be fine, got %v", err)
	}
	for _, d := range []Draft{{Type: ptr("gift")}, {Amount: ptr(-1.0)}, {Date: ptr("08/10/2026")}} {
		if err := checkDraft(d); !errors.Is(err, ErrInvalidDraft) {
			t.Errorf("%+v: want ErrInvalidDraft, got %v", d, err)
		}
	}
}

func TestToSubmitErrorMapsKnownErrors(t *testing.T) {
	err := fmt.Errorf("wrap: %w", transactions.ErrAccountForbidden)
	if got := toSubmitError(err).Code; got != "ACCOUNT_NOT_FOUND" {
		t.Errorf("code %q", got)
	}
	if got := toSubmitError(errors.New("boom")).Code; got != "SUBMIT_FAILED" {
		t.Errorf("fallback code %q", got)
	}
}
