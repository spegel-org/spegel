package httpx

import "strings"

// EncapsulateIPv6Host adds square brackets to URLs with IPv6 host to make parsing work.
func EncapsulateIPv6Host(rawURL string) string {
	schemeEnd := strings.Index(rawURL, "://")
	if schemeEnd == -1 {
		return rawURL
	}
	rest := rawURL[schemeEnd+3:]
	slashIdx := strings.IndexByte(rest, '/')
	host := rest
	if slashIdx != -1 {
		host = rest[:slashIdx]
	}
	if strings.Count(host, ":") >= 2 && !strings.Contains(host, "[") {
		lastColon := strings.LastIndex(host, ":")
		ip := host[:lastColon]
		port := host[lastColon:]
		rawURL = rawURL[:schemeEnd+3] + "[" + ip + "]" + port
		if slashIdx != -1 {
			rawURL += rest[slashIdx:]
		}
	}
	return rawURL
}
