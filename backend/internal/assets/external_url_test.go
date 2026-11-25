package assets

import (
	"strings"
	"testing"
)

func TestNormalizeExternalMediaURLMatchesPersistedHTTPSContract(t *testing.T) {
	result, err := normalizeExternalMediaURL("https://BÜCHER.example:443/media?id=1#preview")
	if err != nil {
		t.Fatalf("normalizeExternalMediaURL: %v", err)
	}
	if result.sourceURL != "https://xn--bcher-kva.example/media?id=1" || result.sourceHost != "xn--bcher-kva.example" {
		t.Fatalf("normalized external URL = %+v", result)
	}
	result, err = normalizeExternalMediaURL("https://example.com/media?token=ok")
	if err != nil || result.sourceURL != "https://example.com/media?token=ok" {
		t.Fatalf("query-preserving URL = %+v, err=%v", result, err)
	}
}

func TestNormalizeExternalMediaURLRejectsUnsupportedOrOversizedURLs(t *testing.T) {
	for _, value := range []string{
		"http://example.com/media",
		"https://user@example.com/media",
		"https://user:password@example.com/media",
		"https://example.com:444/media",
		"ftp://example.com/media",
		"data:text/plain,media",
		"javascript:alert(1)",
		"https://example.com\\@127.0.0.1/media",
		"https://example.com/media?q=" + strings.Repeat("a", 4200),
	} {
		if _, err := normalizeExternalMediaURL(value); err == nil {
			t.Errorf("normalizeExternalMediaURL(%q) succeeded, want invalid URL", value)
		}
	}
}

func TestIsProhibitedRemoteAddressRejectsSpecialUseAndAllowsPublicAddresses(t *testing.T) {
	for _, address := range []string{
		"127.0.0.1", "10.0.0.1", "100.64.0.1", "169.254.169.254", "172.16.0.1",
		"192.0.0.1", "192.0.2.1", "192.168.1.1", "198.18.0.1", "198.51.100.1",
		"203.0.113.1", "224.0.0.1", "::", "::1", "fc00::1", "fe80::1", "ff02::1",
		"2001:db8::1", "2001:2::1", "2001:10::1", "::ffff:127.0.0.1", "fe80::1%eth0", "not-an-ip",
	} {
		if !isProhibitedRemoteAddress(address) {
			t.Errorf("isProhibitedRemoteAddress(%q) = false, want true", address)
		}
	}
	for _, address := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if isProhibitedRemoteAddress(address) {
			t.Errorf("isProhibitedRemoteAddress(%q) = true, want false", address)
		}
	}
}
