package dashboard

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/accounts"
)

var bkk = time.FixedZone("ICT", 7*3600)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, bkk) }

func TestNextDayOfMonth(t *testing.T) {
	cases := []struct {
		today time.Time
		dueOn int
		want  time.Time
	}{
		{day(2026, 10, 7), 15, day(2026, 10, 15)}, // later this month
		{day(2026, 10, 7), 7, day(2026, 10, 7)},   // today counts
		{day(2026, 10, 7), 5, day(2026, 11, 5)},   // passed → next month
		{day(2026, 4, 10), 31, day(2026, 4, 30)},  // clamped to month end
		{day(2026, 1, 31), 30, day(2026, 2, 28)},  // passed + clamped in Feb
		{day(2026, 12, 20), 5, day(2027, 1, 5)},   // year rollover
	}
	for _, c := range cases {
		if got := nextDayOfMonth(c.today, c.dueOn); !got.Equal(c.want) {
			t.Errorf("nextDayOfMonth(%s, %d) = %s, want %s",
				c.today.Format(dateLayout), c.dueOn, got.Format(dateLayout), c.want.Format(dateLayout))
		}
	}
}

func TestNetWorth(t *testing.T) {
	got := netWorth([]accounts.Account{
		{Balance: 1000, MyReportScope: accounts.ReportScopeAll},
		{Balance: -300, MyReportScope: accounts.ReportScopeAll, Type: accounts.TypeCreditCard},
		{Balance: 5000, MyReportScope: accounts.ReportScopeNone}, // hidden wallet
	})
	want := NetWorth{Total: 700, Assets: 1000, Liabilities: 300, AccountsCount: 2}
	if got != want {
		t.Fatalf("netWorth = %+v, want %+v", got, want)
	}
}

func TestBuildUpcoming(t *testing.T) {
	today := day(2026, 10, 7)
	due := 9
	scheduled := []scheduledRow{
		{ID: uuid.New(), Name: "Internet", Type: "expense", Amount: 599, DueDate: "2026-10-05"},
		{ID: uuid.New(), Name: "Salary", Type: "income", Amount: 30000, DueDate: "2026-10-20"},
	}
	accts := []accounts.Account{
		{ID: uuid.New(), Name: "KTC", Type: accounts.TypeCreditCard, Balance: -3200, PaymentDueDate: &due},
		{ID: uuid.New(), Name: "Paid off", Type: accounts.TypeCreditCard, Balance: 0, PaymentDueDate: &due},
	}
	up := buildUpcoming(today, scheduled, accts)

	if len(up.Items) != 3 {
		t.Fatalf("items = %d, want 3 (paid-off card hidden)", len(up.Items))
	}
	first := up.Items[0]
	if first.Name != "Internet" || !first.Overdue || first.DaysUntil != -2 {
		t.Errorf("first = %+v, want overdue Internet at -2 days", first)
	}
	card := up.Items[1]
	if card.Kind != KindCardDue || card.Amount != 3200 || card.DueDate != "2026-10-09" {
		t.Errorf("card = %+v, want 3200 due 2026-10-09", card)
	}
	if up.TotalExpense != 3799 || up.TotalIncome != 30000 {
		t.Errorf("totals = %v / %v, want 3799 / 30000", up.TotalExpense, up.TotalIncome)
	}
}
