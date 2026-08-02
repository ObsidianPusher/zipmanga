package mounts

import "testing"

func TestBuildSMBAddressSupportsIPv6Hosts(t *testing.T) {
	got := buildSMBAddress("2001:db8::10", 445)
	if got != "[2001:db8::10]:445" {
		t.Fatalf("buildSMBAddress returned %q", got)
	}
}
