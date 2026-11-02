package notifications

import "testing"

func TestSettingsCounts(t *testing.T) {
	on, total := SettingsCounts([]string{TypeEventCompleted, "project_added", "unknown"})
	if total != len(mutableTypes) || on != total-1 {
		t.Errorf("counts = %d / %d (mutable %d)", on, total, len(mutableTypes))
	}
	if !mutableTypes[TypeAccountInvite] || mutableTypes["project_added"] {
		t.Error("F2: requests are mutable; project_added is retired")
	}
}

// Owner 2026-10-30 (F1): แจ้ง and อัตโนมัติ are independent: muted = no notice, applied or not.
// Yourself: never.
func TestInsertFollowsTheMute(t *testing.T) {
	muted := Delivery{Deliver: false}
	cases := []struct {
		typ     string
		applied bool
		want    bool
	}{
		{TypeBillNew, false, false},
		{TypeBillNew, true, false},
		{TypeBillChanged, false, false},
		{TypeBillChanged, true, false},
		{TypeBillRemoved, false, false},
		{TypeBillRemoved, true, false},
		{TypeDebtDisconnected, false, false}, // info only: the mute wins
		{TypeRecurringDue, false, false},     // no auto switch
		{TypeBillMoved, true, false},
	}
	for _, c := range cases {
		if got := muted.Insert(c.typ, c.applied); got != c.want {
			t.Errorf("muted Insert(%s, applied=%v) = %v, want %v", c.typ, c.applied, got, c.want)
		}
		if !(Delivery{Deliver: true}).Insert(c.typ, c.applied) {
			t.Errorf("not muted Insert(%s) = false", c.typ)
		}
	}
	if (Delivery{self: true}).Insert(TypeBillNew, false) {
		t.Error("my own change must never notify me")
	}
}
