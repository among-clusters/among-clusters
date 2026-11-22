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
	if len(p.Endpoints) < 1 || len(p.Endpoints) > 32 {
		return errors.New("publicIngress requires 1..32 endpoints")
	}
	seen := map[string]bool{}
	for _, e := range p.Endpoints {
		if seen[e.ID] || !regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$`).MatchString(e.ID) || len(e.ID) > 63 || e.Site == "" || len(e.Site) > 63 || e.Region == "" || len(e.Region) > 63 {
			return errors.New("invalid or duplicate publicIngress endpoint identity")
		}
		seen[e.ID] = true
		ip := net.ParseIP(e.Address)
		u, err := url.ParseRequestURI(e.Path)
		if e.Protocol != "https" || ip == nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || e.Port < 1 || e.Port > 65535 || len(e.Hostname) > 253 || !ingressHostname.MatchString(e.Hostname) || len(e.Path) > 2048 || !strings.HasPrefix(e.Path, "/") || strings.HasPrefix(e.Path, "//") || err != nil || u.IsAbs() || u.Host != "" || e.Revision == "" || len(e.Revision) > 128 {
			return errors.New("invalid HTTPS publicIngress endpoint contract")
		}
	}
	return nil
}
