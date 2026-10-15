package domain

import "testing"

func TestDecimalBoundaryAndIsolation(t *testing.T) {
	for _, input := range []string{"-1", "+1", "1e3", "1,000", "1.", ".1", "0.000000001", "1000000000000000000"} {
		if _, err := ParseDecimal(input); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
	cases := []struct {
		input    string
		exponent int
		want     string
	}{{"999999999999999999.99999999", 8, "99999999999999999999999999"}, {"10.99999999", 2, "1099"}, {"0.00000001", 0, "0"}, {"0", 8, "0"}}
	for _, tc := range cases {
		d, err := ParseDecimal(tc.input)
		if err != nil {
			t.Fatal(err)
		}
		n, err := d.Minor(tc.exponent)
		if err != nil || n.String() != tc.want {
			t.Fatalf("%s: %v %v", tc.input, n, err)
		}
		d.Atoms().SetInt64(999)
		next, _ := d.Minor(tc.exponent)
		if next.String() != tc.want {
			t.Fatal("mutable alias leaked")
		}
	}
	d, _ := ParseDecimal("1")
	for _, e := range []int{-1, 9} {
		if _, err := d.Minor(e); err == nil {
			t.Fatal("invalid exponent")
		}
	}
}
