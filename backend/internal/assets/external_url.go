package assets

import (
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strings"

	"golang.org/x/net/idna"
)

const maximumNormalizedExternalURLBytes = 4096

var ErrExternalURLInvalid = errors.New("external media URL is invalid")

type normalizedExternalURL struct {
	sourceURL  string
	sourceHost string
}

func normalizeExternalMediaURL(input string) (normalizedExternalURL, error) {
	parsed, err := url.Parse(input)
	if err != nil || parsed == nil || parsed.Scheme == "" || parsed.Opaque != "" || parsed.Host == "" {
		return normalizedExternalURL{}, ErrExternalURLInvalid
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "https" || parsed.User != nil {
		return normalizedExternalURL{}, ErrExternalURLInvalid
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return normalizedExternalURL{}, ErrExternalURLInvalid
	}
	host, err := normalizeExternalHostname(parsed.Hostname())
	if err != nil {
		return normalizedExternalURL{}, ErrExternalURLInvalid
	}
	if strings.Contains(host, ":") {
		parsed.Host = "[" + host + "]"
	} else {
		parsed.Host = host
	}
	parsed.Fragment = ""
	parsed.RawFragment = ""
	sourceURL := parsed.String()
	if len([]byte(sourceURL)) > maximumNormalizedExternalURLBytes {
		return normalizedExternalURL{}, ErrExternalURLInvalid
	}
	return normalizedExternalURL{sourceURL: sourceURL, sourceHost: host}, nil
}

func normalizeExternalHostname(hostname string) (string, error) {
	hostname = strings.TrimPrefix(strings.TrimSuffix(hostname, "]"), "[")
	if hostname == "" || strings.ContainsAny(hostname, "/@\\%") {
		return "", ErrExternalURLInvalid
	}
	if ip := net.ParseIP(hostname); ip != nil {
		return strings.ToLower(ip.String()), nil
	}
	ascii, err := idna.Lookup.ToASCII(hostname)
	if err != nil || ascii == "" || strings.ContainsAny(ascii, "/@\\:%") {
		return "", ErrExternalURLInvalid
	}
	return strings.ToLower(ascii), nil
}

func isProhibitedRemoteAddress(value string) bool {
	address, err := netip.ParseAddr(strings.Trim(value, "[]"))
	if err != nil || address.Zone() != "" {
		return true
	}
	address = address.Unmap()
	if address.IsUnspecified() || address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast() || address.IsMulticast() {
		return true
	}
	for _, cidr := range prohibitedRemoteCIDRs {
		if cidr.Contains(address) {
			return true
		}
	}
	return false
}

var prohibitedRemoteCIDRs = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2001:2::/48"),
	netip.MustParsePrefix("2001:10::/28"),
}
