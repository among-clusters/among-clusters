# Security policy

The latest published minor release is supported. Security fixes may be
backported when maintainers identify a broadly deployed older release; any
backport is announced in its GitHub Release notes.

Report vulnerabilities privately through
[GitHub Security Advisories](https://github.com/among-clusters/among-clusters/security/advisories/new).
Do not open a public issue containing exploit details, credentials, private
cluster identities, invitation material, or production topology. Maintainers
will acknowledge a report within seven days and coordinate validation, a CVE
when applicable, a fixed release, and disclosure through the private advisory.

Only tagged images, Helm charts, and Headlamp archives produced by the release
workflow are official. Consumers should verify the keyless signatures,
attestations, checksums, and immutable image digest before deployment.

Automated governance includes Dependabot for Actions, Go modules, containers,
and npm, dependency review on pull requests, CodeQL, and OpenSSF Scorecard.
Findings remain in GitHub Security until coordinated disclosure.
