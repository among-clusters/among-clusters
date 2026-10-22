# Dependency and responsibility map

AmongClusters records and transports explicit collaboration contracts between
autonomous Kubernetes clusters. It does not become the Kubernetes manager,
identity provider, scheduler, public edge manager, or general service mesh.

| Component | Required | What AmongClusters consumes or produces | What remains outside this project |
| --- | --- | --- | --- |
| Kubernetes API | Yes | Stores peering CRDs; reads explicitly advertised Services; creates narrowly scoped grant RBAC when locally approved | Cluster lifecycle, workload reconciliation, Secrets, scheduling, and general API access |
| Hub | Yes for rendezvous | Validates invitations, signed control messages, policies, peer state, and advertisements | Tenant identity lifecycle and application data |
| Owner-side Agent | Yes per member cluster | Retains local identity, observes bounded metadata, sends signed snapshots | Cluster administration and remote credentials |
| QUIC Gateway | Required for imported traffic | Carries explicitly authorized streams over TLS 1.3 mTLS | Pod networking, PodCIDR exchange, arbitrary east-west routing, and public ingress |
| Credential Broker | Optional | Holds encrypted, opaque references for explicitly approved managed-access grants | Browser-held credentials and general secret storage |
| Headlamp | Optional | Displays read-only peering state and consumes one-time onboarding documents | Authentication, RBAC authority, and remote-cluster proxying outside approved grants |
| CiliumNetworkPolicy | Optional | Narrows known control and data paths when Cilium is present | Cilium installation, routing, BGP, and cluster-wide policy |
| Public edge or DNS controller | Optional | Publishes the Hub only when operators explicitly configure the chart integration | DNS authority, Gateway listeners, certificates, NAT, BGP, and public IP ownership |
| Flux or another GitOps engine | Recommended | Applies reviewed chart configuration | Git reconciliation and repository policy |

## Trust boundary

```text
Owner cluster                         Hub control plane
┌──────────────────────┐             ┌────────────────────────┐
│ Agent                │ signed HTTPS│ desired peer contracts │
│ - private key stays  ├────────────►│ public keys and status │
│ - bounded summaries  │             └────────────────────────┘
│                      │
│ Gateway              │ mTLS/QUIC       Peer Gateway
│ - explicit service   ├──────────────────────────────►
│ - explicit peer ID   │
└──────────────────────┘
```

No Kubernetes object bodies, Secrets, Pod addresses, EndpointSlices, or
implicit service routes cross this boundary. A configured public endpoint is
only an endpoint claim; AmongClusters does not create its DNS, listener, NAT,
firewall, or BGP state.
