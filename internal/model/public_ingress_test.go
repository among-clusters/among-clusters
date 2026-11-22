package model

import "testing"

func TestPublicIngressRequiresTypedHTTPSContract(t *testing.T) {
	good := PublicIngressEndpoint{ID: "site-a", Site: "site-a", Region: "test-region", Protocol: "https", Address: "192.0.2.10", Port: 443, Hostname: "canary.example.com", Path: "/health", Revision: "reviewed-1"}
	if err := ValidatePublicIngress(&PublicIngress{Endpoints: []PublicIngressEndpoint{good}}); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*PublicIngressEndpoint){func(p *PublicIngressEndpoint) { p.Protocol = "quic" }, func(p *PublicIngressEndpoint) { p.Address = "https://192.0.2.10" }, func(p *PublicIngressEndpoint) { p.Port = 0 }, func(p *PublicIngressEndpoint) { p.Path = "//other.example.com" }, func(p *PublicIngressEndpoint) { p.Revision = "" }} {
		p := good
		mutate(&p)
		if ValidatePublicIngress(&PublicIngress{Endpoints: []PublicIngressEndpoint{p}}) == nil {
			t.Fatalf("accepted invalid ingress %#v", p)
		}
	}
}

func TestPublicIngressEndpointIDsAreStableUniqueAndOrderedIndependently(t *testing.T) {
	a := PublicIngressEndpoint{ID: "site-a", Site: "site-a", Region: "region-a", Protocol: "https", Address: "192.0.2.10", Port: 443, Hostname: "canary.example.com", Path: "/health", Revision: "r1"}
	b := a
	b.ID = "site-b"
	b.Site = "site-b"
	b.Region = "region-b"
	b.Address = "192.0.2.11"
	for _, endpoints := range [][]PublicIngressEndpoint{{a}, {a, b}, {b, a}, {b}} {
		if err := ValidatePublicIngress(&PublicIngress{Endpoints: endpoints}); err != nil {
			t.Fatal(err)
		}
	}
	if ValidatePublicIngress(&PublicIngress{Endpoints: []PublicIngressEndpoint{a, a}}) == nil {
		t.Fatal("duplicate endpoint IDs accepted")
	}
}
