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
The diagnostics do not alter authentication, grant expiry, key lifecycle, route
selection, HTTP timeout budgets or the QUIC/TCP Gateway protocol. The L4 Gateway
is not claimed as an instrumented HTTP hop.

No new public metrics endpoint, HTTPRoute, scrape permission or NetworkPolicy
allowance is introduced. Runtime delivery still needs the existing owner to
configure the DSN reference and reviewed Sentry egress, then prove actual event
and same-trace ingestion. A signed artifact or an enabled value alone is not
runtime acceptance.

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
