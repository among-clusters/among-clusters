# Contributing

AmongClusters is an independent component. Contributions must keep defaults
portable and must not add organization-specific domains, addresses,
credentials, tenant data, or assumptions about a particular cluster fleet.

Before opening a pull request, run:

```sh
go test ./...
go vet ./...

digest=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
helm lint charts/among-clusters-hub --set image.digest="$digest"
helm lint charts/among-clusters-agent \
  --set clusterID=test --set tenant=test --set trustDomain=test.example \
  --set image.digest="$digest"
helm lint charts/among-clusters-headlamp \
  --set headlamp.pluginsPVC.existingClaim=plugins
helm dependency build charts/among-clusters
helm lint charts/among-clusters \
  --set agent.enabled=true \
  --set among-clusters-agent.clusterID=test \
  --set among-clusters-agent.tenant=test \
  --set among-clusters-agent.trustDomain=test.example \
  --set among-clusters-agent.image.digest="$digest"

(cd ui/headlamp-plugin && pnpm install --frozen-lockfile && pnpm run tsc && pnpm run lint && pnpm run build)
python3 scripts/validate_artifacts.py
docker build .
```

Keep one concern per pull request. Commits and release tags must be verified,
and releases are produced only by the repository release workflow.

By contributing, you agree that your contribution is licensed under
Apache-2.0.
