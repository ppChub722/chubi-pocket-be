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
