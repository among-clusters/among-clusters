# Managed public ingress source lease

A producer may project eligible local public endpoints into a Service's existing
`peering.re8ch.com/public-ingress` annotation and attach these source annotations:

- `networking.re8ch.com/public-ingress-owner`: the local Among Agent cluster ID.
- `networking.re8ch.com/public-ingress-source-uid`: the Service's current UID.
- `networking.re8ch.com/public-ingress-valid-until`: an RFC3339 deadline renewed
  by the producer after a successful current observation/projection.

The Agent validates managed annotations when the owner key is present. Empty or
foreign owner, missing/mismatched UID, missing/malformed/expired deadline, or a
deadline more than 86400 seconds in the future omits that Service's PublicIngress.
The future bound reuses the existing service-advertisement TTL protocol maximum;
it adds no new user quota or configuration knob. Owner, UID and validity do not
become advertisement spec fields or get extended by the Agent's heartbeat.

Malformed managed JSON, unknown fields or invalid endpoints likewise omit only
that public capability. The ordinary Service stays in the complete snapshot,
retaining protocol, service class, peer policy, targets and normal QUIC route.
Other Services are still published. A Service recreated under the same name
must receive a new projection with its actual new UID before managed ingress is
accepted again. Endpoint revisions remain producer-owned.

Only an unchanged endpoint contract is sent while the producer renews a source
lease. Consequently a heartbeat renewal alone does not change the advertisement
specification/generation. If the producer stops, its existing deadline expires
and the next Agent snapshot removes that PublicIngress instead of indefinitely
renewing old public capability.

Annotations without the owner key retain the existing reviewed manual/canary
model and validation behavior. They are not stamped as managed PE health or
upgraded into new conformance evidence. Service/peer ownership, ordinary service
identity, remote credentials and authorization are unchanged by this filter.
