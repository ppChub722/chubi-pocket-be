package notifications

import "testing"

// S4 / X9: auto follows the type's sync switch, whatever the mute.
func TestAutoIndependentOfMute(t *testing.T) {
	on := &Settings{MutedTypes: []string{TypeProjectTxChanged}, AutoSyncEvents: true}
	if on.Deliver(TypeProjectTxChanged) || !on.Auto(TypeProjectTxChanged) {
		t.Error("muted + events switch on → applied silently")
	}
	st := &Settings{
		MutedTypes:     []string{TypeProjectTxChanged},
		AutoSyncEvents: false,
		AutoSyncDebts:  true,
	}
	cases := []struct {
		typ           string
		deliver, auto bool
	}{
		{TypeProjectTxChanged, false, false}, // muted + events switch off → nothing
		{TypeSharedBillCreated, true, false}, // shared bills pick the switch by bill kind (Delivery.Sync*)
		{TypeRecurringDue, true, false},      // no switch
		{TypeDebtDisconnected, true, false},  // info only
	}
	for _, c := range cases {
		if got := st.Deliver(c.typ); got != c.deliver {
			t.Errorf("Deliver(%s) = %v, want %v", c.typ, got, c.deliver)
		}
		if got := st.Auto(c.typ); got != c.auto {
			t.Errorf("Auto(%s) = %v, want %v", c.typ, got, c.auto)
		}
	}
}

func TestSettingsCounts(t *testing.T) {
	on, total := SettingsCounts([]string{TypeProjectAdded, "unknown"})
	if total != len(mutableTypes) || on != total-1 {
		t.Errorf("counts = %d / %d (mutable %d)", on, total, len(mutableTypes))
	}
	if mutableTypes[TypeAccountInvite] {
		t.Error("invites must not be mutable")
	}
}

// Owner 2026-10-28: a notice that still asks (auto off → not applied) always comes, muted or not;
// muting silences only what auto already did. Other types follow the mute; yourself: never.
func TestInsertAskingNoticeIgnoresMute(t *testing.T) {
	muted := Delivery{Deliver: false}
	cases := []struct {
		typ     string
		applied bool
		want    bool
	}{
		{TypeSharedBillCreated, false, true}, // asks "เพิ่มในสมุด"
		{TypeSharedBillCreated, true, false}, // auto-accepted: silent
		{TypeProjectTxChanged, false, true},  // asks "อัปเดตตาม"
		{TypeProjectTxChanged, true, false},  // auto-updated: silent
		{TypeSharedBillDeleted, false, true}, // asks to remove my copy
		{TypeSharedBillDeleted, true, false}, // removed: silent
		{TypeDebtDisconnected, false, false}, // info only: the mute wins
		{TypeRecurringDue, false, false},     // no auto switch
		{TypeSharedBillDisconnected, true, false},
	}
	for _, c := range cases {
		if got := muted.Insert(c.typ, c.applied); got != c.want {
			t.Errorf("muted Insert(%s, applied=%v) = %v, want %v", c.typ, c.applied, got, c.want)
		}
		if !(Delivery{Deliver: true}).Insert(c.typ, c.applied) {
			t.Errorf("not muted Insert(%s) = false", c.typ)
		}
	}
	if (Delivery{self: true}).Insert(TypeSharedBillCreated, false) {
		t.Error("my own change must never notify me")
	}
}
