package projects

import (
	"reflect"
	"testing"
)

func TestNormalizeTags(t *testing.T) {
	cases := []struct {
		in   []string
		want []string
	}{
		{nil, []string{}},
		{[]string{"Food", " food ", "FOOD"}, []string{"Food"}},
		{[]string{"  ", "", "Taxi"}, []string{"Taxi"}},
		{[]string{"อาหาร", "เดินทาง", "อาหาร"}, []string{"อาหาร", "เดินทาง"}},
	}
	for _, c := range cases {
		if got := normalizeTags(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("normalizeTags(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
