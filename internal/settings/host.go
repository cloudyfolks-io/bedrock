package settings

import "strings"

func PlatformHost(vip, host string) string {
	if host != "" {
		return host
	}
	return strings.ReplaceAll(vip, ".", "-") + ".sslip.io"
}
