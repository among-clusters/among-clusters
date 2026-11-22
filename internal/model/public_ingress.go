package model

import (
	"errors"
	"net"
	"net/url"
	"regexp"
	"strings"
)

var ingressHostname = regexp.MustCompile(`^(?:[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)+[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

// ValidatePublicIngress never interprets a QUIC gateway endpoint as public HTTPS.
func ValidatePublicIngress(p *PublicIngress) error {
	if p == nil {
		return nil
	}
	ip := net.ParseIP(p.Address)
	u, err := url.ParseRequestURI(p.Path)
	if p.Protocol != "https" || ip == nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || p.Port < 1 || p.Port > 65535 || len(p.Hostname) > 253 || !ingressHostname.MatchString(p.Hostname) || len(p.Path) > 2048 || !strings.HasPrefix(p.Path, "/") || strings.HasPrefix(p.Path, "//") || err != nil || u.IsAbs() || u.Host != "" || p.Revision == "" || len(p.Revision) > 128 {
		return errors.New("invalid HTTPS publicIngress contract")
	}
	return nil
}
