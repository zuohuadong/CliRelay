package util

import "testing"

func TestConstantTimeStringEqual(t *testing.T) {
	if !ConstantTimeStringEqual("secret", "secret") {
		t.Fatal("expected identical values to match")
	}
	if ConstantTimeStringEqual("secret", "different") {
		t.Fatal("expected different values not to match")
	}
	if ConstantTimeStringEqual("secret", "secret-longer") {
		t.Fatal("expected different length values not to match")
	}
}
