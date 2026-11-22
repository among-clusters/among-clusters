package model

import "testing"

func TestPublicIngressRequiresTypedHTTPSContract(t *testing.T) {
	good := PublicIngress{Protocol: "https", Address: "192.0.2.10", Port: 443, Hostname: "canary.example.com", Path: "/health", Revision: "reviewed-1"}
	if err := ValidatePublicIngress(&good); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*PublicIngress){func(p *PublicIngress) { p.Protocol = "quic" }, func(p *PublicIngress) { p.Address = "https://192.0.2.10" }, func(p *PublicIngress) { p.Port = 0 }, func(p *PublicIngress) { p.Path = "//other.example.com" }, func(p *PublicIngress) { p.Revision = "" }} {
		p := good
		mutate(&p)
		if ValidatePublicIngress(&p) == nil {
			t.Fatalf("accepted invalid ingress %#v", p)
		}
	}
}
