package projects

import "testing"

func TestCleanTagName(t *testing.T) {
	cases := map[string]string{
		"  Food ": "Food",
		"":        "",
		"อาหาร":   "อาหาร",
		"abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzXYZ": "abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwx",
	}
	for in, want := range cases {
		if got := cleanTagName(in); got != want {
			t.Errorf("cleanTagName(%q) = %q, want %q", in, got, want)
		}
	}
}
