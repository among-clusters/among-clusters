# AmongClusters

**Mesh clusters you don't control.**

AmongClusters is a Sovereign Cluster Peering control plane for BYOC services.
Each Kubernetes cluster remains an independent trust domain: it retains its API
server, CA, RBAC, scheduler and lifecycle authority. The hosted Hub distributes
signed identities, trust bundles, gateway endpoints and explicit service
advertisements; it does not receive Kubernetes credentials and is not in the
default application data path.

## Trust model

The 0.3.33 release carries per-Service public ingress endpoints and accepted
dynamic export leases. Upgrade the Hub before Agents: Agents require the Hub's
publication receipt before renewing dynamic routes. Verify new-stream withdrawal
and expiry separately from existing-stream draining when accepting the release.

- An owner-side Agent creates and retains cluster identity material locally.
- Invitations are tenant-bound, short-lived and single-use; the Hub stores only
  their hashes.
- A Peer becomes eligible only after both owners confirm the other's bundle
  digest.
- Gateways use TLS 1.3 mutual authentication over QUIC and accept certificates
  only from explicitly installed peer bundles with SPIFFE-compatible identities.
- v1 advertises selected L4/L7 services only. It does not synchronize PodCIDRs,
  EndpointSlices, Pod addresses or remote API secrets.
- `ManagedAccessGrant` is an optional, separately gated contract. It is disabled
  by default and stores only an opaque Credential Broker reference.

The API group is `peering.re8ch.com/v1alpha1`. Its namespace-scoped resources are
`ClusterIdentity`, `Peer`, `AuthenticatedLink`, `PeerPolicy`,
`ServiceAdvertisement`, `ImportedService` and `ManagedAccessGrant`.

## Charts

Four OCI charts are released independently under `ghcr.io/among-clusters/charts`:

- `among-clusters`: umbrella chart with role switches.
- `among-clusters-hub`: rendezvous API, controller, CRDs and audit control plane.
- `among-clusters-agent`: owner-side identity Agent and QUIC Gateway.
- `among-clusters-headlamp`: installer for the digest-checked read-only plugin.

Images must be configured by immutable `sha256:` digest. The Agent receives a
projected Kubernetes token and namespace-local read access to explicitly labelled
Services. The Gateway is a separate pod with no service-account token; its UDP
listener is off until a public endpoint and a confirmed peer-bundle Secret are
configured.

For coordination-only edge clusters, set `gateway.enabled=false`. This installs
only the signing Agent: it publishes explicitly annotated Service contracts and
heartbeats over outbound HTTPS, while omitting the QUIC Gateway Deployment and
Service. This mode never grants managed Kubernetes access and is the preferred
bootstrap for GitOps-controlled traffic-distribution clusters.

## Explicit service and managed access flow

A Service is exported only when it has `peering.re8ch.com/advertise=true` plus
the `protocol`, `service-class`, `policy-ref`, `target-peers` and `ttl-seconds`
annotations. The Agent sends the full contract in a signed `service.snapshot`;
the Hub accepts it only when the referenced `PeerPolicy` permits the publisher,
peer, class, protocol, port and export direction. Removed labels revoke the
advertisement. The QUIC Gateway routes streams by SPIFFE service identity and
never exposes Pod or EndpointSlice addresses. Each export also carries its
target Peer refs; `gateway.peerIdentities` binds those refs to allowed caller
SPIFFE IDs so trust-bundle membership alone never grants service access.

A provider may deliberately advertise to every approved peer by setting
`peerSelector.matchAllReady=true` on its `PeerPolicy` and `target-peers="*"` on
the Service. The Hub expands the wildcard only to non-suspended `Peer` objects
whose status is `Ready` and which include the publisher. At the Gateway, the
wildcard admits only client identities whose certificates validate against the
explicitly installed peer trust bundle; it does not permit anonymous clients.

Managed Kubernetes access requires both `spec.approved=true` on the Hub grant
and an owner-created `among-clusters-approval-<grant>` ConfigMap in the BYOC
cluster. Only then does the Agent create namespace Roles, or a rule-defined
ClusterRole when the grant explicitly sets `scope: Cluster`, issue a short-lived
TokenRequest, encrypt it to the Broker X25519 public key and submit it directly
to the Broker. The Hub stores only
`credential://among-clusters/<tenant>/<grant>`. Headlamp uses the Broker's
internal Kubernetes proxy; browsers never receive the Kubernetes token.
The local approval records the grant's Kubernetes generation, so every change
to scope, rules, target or expiry requires a fresh owner approval.

The Broker namespace is an operator-owned prerequisite. The Hub chart deploys
namespaced Broker resources into `managedAccess.broker.namespace`, but never
creates or owns that Namespace. This prevents Helm rollback or uninstall from
deleting unrelated credential brokers and their state.

## Breaking migration from 0.1

Version 0.2.0 replaces the experimental `Collaboration*` API. Helm never deletes
the old CRDs automatically. First export and review the old objects, then perform
the explicit destructive upgrade with `migration.replaceCollaborationCRDs=true`.
The migration Job emits sanitized object specifications before deleting the four
legacy CRDs. Secrets and status payloads are not exported. Keep the logs as the
review artifact before initializing new peering objects.

## Headlamp

`ui/headlamp-plugin` is an independent, read-only Headlamp package. It displays
trust domains, peers, authenticated links, advertisements, imported services,
certificate/bundle state and failure reasons. It has no mutations, Secret reads,
credential rendering or remote Kubernetes operations. The native Headlamp
Artifact Hub metadata lives under `artifacthub/headlamp`.

Release tags publish multi-architecture images with SBOM/provenance, all four
OCI charts, signatures, Artifact Hub repository metadata, and the versioned
Headlamp archive with its SHA-256 metadata.

## Optional reviewed public HTTPS ingress

A Service selected for advertisement may additionally carry the reviewed annotation
`peering.re8ch.com/public-ingress`, containing one JSON object:

```json
{"endpoints":[{"id":"site-a","site":"site-a","region":"region-a","protocol":"https","address":"192.0.2.10","port":443,"hostname":"canary.example.com","path":"/health","revision":"reviewed-1"}]}
```

The agent includes this typed contract in its signed service snapshot. The hub
validates it and preserves it as `ServiceAdvertisement.spec.publicIngress`; the
existing import reconciler projects the currently observed contract and source
UID/generation/resourceVersion into `ImportedService.status`. Expired, revoked,
missing or non-targeted advertisements cannot produce a Ready import contract.
The public ingress is distinct from the local Service port and QUIC
`gatewayEndpoints`; no QUIC URL is interpreted as an HTTPS address.

Consumers must bind exact advertisement/import UIDs, publisher, service identity
and protocol, evaluate publication time plus TTL locally, and reject a stale
import revision. A changed ingress must be probed independently before use;
old endpoint health must not carry across address changes. Admission of these
facts does not replace the consumer's own AF path evidence or DNS/TLS policy.
Existing services without the annotation retain their prior transport contract.

Each advertisement keeps one real Service identity and publisher owner while
`publicIngress.endpoints` exposes up to 32 reviewed site endpoints. Stable unique
IDs survive list reordering; consumers select an explicit endpoint ID. This is
not permission for a publisher to claim another cluster's relay ownership.

## Dynamic export leases and source withdrawal

The agent renews dynamic gateway routes only after the hub accepts its signed
service snapshot and returns its exact control generation, publication timestamp,
and per-Service approved peer references. Wildcard requests become the hub's
actual Ready peer subset; empty, missing or mismatched receipts fail closed. The
agent writes only these approved references, never a requested wildcard. Route expiry is
that timestamp plus the reviewed Service TTL, not a later local file-write time.
A rejected/failed snapshot does not install or renew a route. An older hub without
a publication receipt is not treated as acceptance for this lease protocol.
Deploy the new hub before the new agent and verify fresh leases before acceptance.

Each newly opened QUIC service stream checks the current dynamic route lease and
target-peer authorization before connecting upstream. An expired or revoked dynamic
identity cannot fall back to a same-named startup static export. The agent persists
scoped tombstones for removed dynamic identities in the existing route ConfigMap;
they survive process restarts and do not disable unrelated explicitly reviewed
static routes. Republish an identity only through a newly accepted source snapshot.

A transient route-file failure can retain only a still-valid accepted lease; it
cannot extend expiry or silently downgrade a known leased identity to unlimited
legacy routing. Do not delete tombstone history to roll back. Existing unleased
legacy dynamic files are compatibility input, not evidence of lease enforcement;
verify the new agent has written leaseRequired/expiresAt/acceptedGeneration.

These checks govern **new streams**. Streams already copying data keep the current
half-close/drain behavior; no established business stream is forcibly terminated,
and no operation or stream payload is automatically replayed. Strong active-stream
revocation would require a separately reviewed lifecycle policy. Unrelated peer
session heartbeats continue when a service export is withdrawn.
