# Exact-object managed access

ManagedAccessGrant rules preserve Kubernetes `resourceNames` through the CRD,
Hub instruction and Agent-generated Role/ClusterRole. This supports a separate
release identity scoped to an explicitly reviewed namespace and object instead
of giving automation an interactive administrator's cluster-wide grant.

```yaml
scope: Namespaced
namespaces: [default]
rules:
  - apiGroups: [""]
    resources: [configmaps]
    resourceNames: [re8ch-cloud-target-canary]
    verbs: [get, patch, update, delete]
```

The ordinary Kubernetes RBAC semantics apply: absent/empty resourceNames is
unrestricted by name; this field does not make ordinary CREATE name-scoped.
Release consumers must validate the exact required names and verbs before use.
Local owner approval remains bound to the grant generation. A new scoped release
grant is distinct from an existing interactive administrator grant.

New Agents negotiate signed `grant.poll.v2` and require protocolVersion=2 in its
response before reconciling any grants. Upgrade the Hub before Agents. An older
Hub does not satisfy that protocol; Agents do not silently fall back and discard
restrictions. Existing rules without resourceNames keep their original meaning.

The Hub projects exact-name grants to legacy `grant.poll` clients only as revoked
cleanup instructions, with no active rules. This removes installed broader roles
and broker credentials for that grant instead of allowing a legacy JSON decoder
to ignore the new field and widen access. The owner's grant object is unchanged;
only a v2-capable Agent may install its scoped rules. This also allows cleanup
after an Agent downgrade. Other grants remain independent.

The CRD uses Kubernetes structural set semantics for string-set fields rather
than unsupported uniqueItems=true. Bootstrap the additive resourceNames schema
before creating a scoped grant: admission pruning must never erase its scope.

Runtime acceptance must use the actual release identity to PATCH the named
canary through the managed cluster connection and demonstrate denial for a
different object, Secret access and an unreviewed cluster target. Verify the
remote generated Role and actual broker-backed operation, not just approval or
directory readiness. Remote credentials stay in the existing broker.
