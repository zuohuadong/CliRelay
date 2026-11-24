package cliproxy

import "testing"

func TestSanitizePprofAddrDefaultsToLoopback(t *testing.T) {
	got := sanitizePprofAddr(":9000", false)
	if got != "127.0.0.1:9000" {
		t.Fatalf("addr = %q", got)
	}
}

func TestSanitizePprofAddrForcesLoopbackWhenRemoteDisallowed(t *testing.T) {
	got := sanitizePprofAddr("0.0.0.0:9000", false)
	if got != "127.0.0.1:9000" {
		t.Fatalf("addr = %q", got)
	}
}

func TestSanitizePprofAddrPreservesRemoteWhenAllowed(t *testing.T) {
	got := sanitizePprofAddr("0.0.0.0:9000", true)
	if got != "0.0.0.0:9000" {
		t.Fatalf("addr = %q", got)
	}
}
