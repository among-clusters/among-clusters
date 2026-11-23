# HTTP diagnostic coverage

Hub and credential Broker optionally use the official Sentry Go SDK. Set
`SENTRY_DSN`, `SENTRY_RELEASE` and `SENTRY_ENVIRONMENT` through reviewed runtime
configuration. The chart's `sentry` values are disabled by default. Choose the official public
DSN value (`sentry.dsn`, not an API bearer) or an existing DSN Secret reference
(`sentry.dsnSecretRef`) in the Hub and Broker namespaces. No new Secret or
controller is required for a public DSN; setting both is rejected. Set the
release to `among-clusters@<verified image source SHA>`; do not use a later
GitOps revision or a release-automation token in a Pod. Empty or invalid DSNs
preserve business availability and local operation response identifiers.

Each non-health HTTP request owns an isolated hub and transaction. The same
strictly validated `sentry-trace` and `X-Re8ch-Operation-Id` contract is used by
Cloud: lowercase nonzero hexadecimal trace/span identifiers, and `op_` followed
by a UUID or 32 hexadecimal digits. Invalid identifiers are replaced, arbitrary
incoming baggage is ignored, and all request traces/errors are sampled. The
response returns the root operation identifier. Trace metadata is sent to the
existing Hub HTTP call; local/remote Kubernetes transports explicitly remove it.

Fixed stages cover Hub HTTP, local Kubernetes client requests, credential
availability/decoding, grant authorization, remote Kubernetes proxy transport and
response delivery. The first failure per request is captured once with safe
stack frames and operation correlation. Streaming bodies keep their original
read/close lifecycle and flush behavior; cancellation propagates without replay.
The Broker identity lookup now uses its existing caller context rather than
losing cancellation in `http.Client.Get`. Each proxy request closes only its own
transport idle connections after the entire proxy/stream returns. Active or
shared transports are not closed, and no pool/concurrency/TTL limit is added.

No credentials, authorization headers, user/tenant identity, body, arguments,
result, resource path, URL, hostname, attachments or frame variables are sent.
Sentry's normal asynchronous transport is not flushed on the request path.
A dedicated zero-prefix JSON completion logger emits UTC timestamp, traceId,
operationId, sentryEventId, release, component, environment, errorCategory and
durationMs for native log/trace correlation. Existing unrelated logs keep their
format, and trace/operation IDs are not metric labels.
The diagnostics do not alter authentication, grant expiry, key lifecycle, route
selection, HTTP timeout budgets or the QUIC/TCP Gateway protocol. The L4 Gateway
is not claimed as an instrumented HTTP hop.

## Internal unified metrics

Hub and Broker expose the same-process metrics listener on their explicit
Downward API `POD_IP:9090`; an absent, invalid or wildcard address falls back to
`127.0.0.1:9090`. The application mux on 8080 does not serve `/metrics`. Existing
Services add the named `metrics` port 9090 and stable service labels; public
HTTPRoutes continue targeting only application HTTP port 80/8080. No second
Service, sidecar, daemon or collector is created.

`re8ch_toolchain_http_requests_total` and
`re8ch_toolchain_http_request_duration_seconds` share exactly `component`,
`environment`, `phase`, `method`, `status_class`, `outcome`, `error_category`.
Phases and categories are bounded enums; unknown HTTP methods become `OTHER`.
Normal implicit HTTP responses record the actual 200 status, while cancellation
or panic before a response remains `none`, not an invented 200/499. Duration
buckets extend through 120/300/600/1800/3600 seconds. The build-info gauge uses
only `component`, `release`, `repository`, `environment`. No trace/operation/event ID or exemplar is written to metrics: the current
VictoriaMetrics backend does not support exemplars. Use component, release and
time in metrics with native Grafana links to the correlated JSON logs/Sentry.

Ordinary Pod-network ingress policies admit only the existing
`observability-system` vmagent (`app.kubernetes.io/name=vmagent`,
`app.kubernetes.io/instance=re8ch`) to 9090. These Pod policies do **not** claim to
protect hostNetwork listeners. The existing Hub hostNetwork override must be
read back as a private management/WireGuard POD_IP/listener, and direct public
9090 access must be proved unavailable. That private management-plane boundary
is distinct from Pod-policy enforcement; this change does not modify node
firewalls, hostNetwork, RouterOS or public routing.

The observability owner must deliver the reviewed ServiceScrapes and verify
actual targets and samples, as well as Sentry event/same-trace ingestion and
source/build metadata. A signed artifact, enable flag or healthy Pod alone is
not runtime acceptance.

## Native source correlation

The existing tag release workflow optionally uses the official pinned Sentry
release action after artifact publication, with full history and
`among-clusters@<exact source SHA>`. Hub and Broker share this artifact release
and retain distinct `component` tags. It does not record a production deploy.
Configure approved `SENTRY_AUTH_TOKEN` in GitHub Secrets, non-secret `SENTRY_ORG`
and `SENTRY_PROJECT` Variables, and enable `SENTRY_RELEASE_ENABLED` only after
the native integration registers `among-clusters/among-clusters`. No release
automation token is passed to runtime. A metadata-only failure/backfill must
not trigger rebuilding an existing immutable image: use the official Release
API for that source and verify native commit/PR readback separately.

## Sentry egress capability

When Sentry is explicitly enabled, the existing Hub/Broker Kubernetes
NetworkPolicies allow public TCP 443 for IPv4 and IPv6 so the official SDK can
reach its configured HTTPS ingestion endpoint. This is a deliberate layer-4
public HTTPS allowance, **not** an FQDN restriction. Kubernetes NetworkPolicy
cannot express a Sentry hostname. The original Hub IPv4 world-443 rule remains
unchanged; the Broker previously had only namespace destinations and therefore
could not reliably deliver public Sentry traffic. Existing DNS and API rules
remain unchanged, and no new CNP, inventory permission or network controller is
introduced. Cilium and Kubernetes allow rules are additive; adding a narrow
FQDN policy alone would not narrow the existing broad Hub rule. Actual policy
union and event-ingestion readback remain runtime acceptance requirements.

## Native artifact build

BuildKit builds the five static Go binaries on `BUILDPLATFORM`, targeting its
explicit `TARGETOS`/`TARGETARCH` with CGO disabled, rather than running the Go
compiler under QEMU. The existing multi-platform image/final distroless stage,
signing and provenance pipeline remain in place. Both linux/amd64 and linux/arm64
ELF machine identifiers are validated before publication; an existing source
artifact is never rebuilt merely to change telemetry metadata.
