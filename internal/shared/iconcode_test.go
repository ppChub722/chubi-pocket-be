package shared

import (
	"encoding/json"
	"testing"
)

func TestIconCodeShapeRoundTrip(t *testing.T) {
	var ic IconCode
	if err := json.Unmarshal([]byte(`{"icon":"home","iconColors":[],"shape":"leaf"}`), &ic); err != nil {
		t.Fatal(err)
	}
	if ic.Shape == nil || *ic.Shape != "leaf" {
		t.Fatalf("shape = %v, want leaf", ic.Shape)
	}
	out, _ := json.Marshal(ic)
	var back map[string]any
	_ = json.Unmarshal(out, &back)
	if back["shape"] != "leaf" {
		t.Fatalf("marshalled shape = %v, want leaf", back["shape"])
	}
}

func TestIconCodeUnknownShapeDropped(t *testing.T) {
	var ic IconCode
	if err := json.Unmarshal([]byte(`{"icon":"home","shape":"triangle"}`), &ic); err != nil {
		t.Fatal(err)
	}
	if ic.Shape != nil {
		t.Fatalf("unknown shape kept: %v", *ic.Shape)
	}
	if ic.Icon == nil || *ic.Icon != "home" {
		t.Fatal("other fields must still decode")
	}
}

func TestIconCodeNoShapeOmitted(t *testing.T) {
	icon := "home"
	out, _ := json.Marshal(IconCode{Icon: &icon})
	var back map[string]any
	_ = json.Unmarshal(out, &back)
	if _, ok := back["shape"]; ok {
		t.Fatal("nil shape must be omitted so old rows stay identical")
	}
}
