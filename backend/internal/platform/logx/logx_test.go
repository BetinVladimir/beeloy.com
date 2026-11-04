package logx

import "testing"

func TestMaskEmail(t *testing.T) {
	if got := MaskEmail("alice@example.com"); got != "a***@example.com" {
		t.Fatal(got)
	}
	if MaskEmail("nope") != "***" || MaskEmail("@x") != "***" {
		t.Fatal("bad input must be fully masked")
	}
}
