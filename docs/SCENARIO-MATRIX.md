# Peering and service scenario matrix

Use two independent axes when evaluating a deployment:

- **Trust state**: invited, confirmed, or revoked.
- **Transport state**: unavailable, control-only, or data-ready.

| Trust \ Transport | Unavailable | Control-only | Data-ready |
| --- | --- | --- | --- |
| **Invited** | Keep the invitation bounded and single-use; no Peer is ready. | Accept only onboarding/control exchange; do not import services. | Do not route data before both owners confirm bundle digests. |
| **Confirmed** | Preserve diagnostic state and fail closed; do not invent another route. | Reconcile signed health and advertisements, while imported traffic remains unavailable. | Admit only policy-allowed services and caller identities over the authenticated session. |
| **Revoked or suspended** | Retain auditable status without retrying access. | Reject control updates except the bounded revocation path. | Terminate sessions and withdraw imported services; never retain a stale data route. |

## Facts that change the result

Peering readiness requires all of the following:

```text
ready = peerConfirmedByBothOwners
     && peerBundleInstalled
     && certificateValid
     && persistentSessionHealthy
```

Service authorization is evaluated separately:

```text
authorized = ready
          && advertisementTargetsPeer
          && peerPolicyAllowsDirectionClassProtocolAndPort
          && callerSPIFFEIDMatchesPeerBinding
```

Managed Kubernetes access additionally requires an approved Hub grant, a
matching owner-created local approval at the current generation, and an
unexpired TokenRequest. A healthy peering session never implies managed access.

## Reproduce locally

Run deterministic unit and chart-rendering tests without a cluster:

```sh
go test ./...
go vet ./...
digest=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
helm template hub charts/among-clusters-hub --set image.digest="$digest" >/tmp/hub.yaml
helm template agent charts/among-clusters-agent \
  --set clusterID=test --set tenant=test --set trustDomain=test.example \
  --set image.digest="$digest" >/tmp/agent.yaml
```

Production evaluation should use synthetic service identities and non-sensitive
endpoints. Never commit invitations, private keys, kubeconfigs, tokens, raw
tenant data, or production topology as fixtures.
