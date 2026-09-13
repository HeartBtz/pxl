# Security Policy

## Supported Versions

Security fixes are made on the latest revision of the default branch. Until the
project publishes versioned releases, older commits and private forks are not
maintained by the project.

## Reporting A Vulnerability

Do not open a public issue for a suspected vulnerability. Use GitHub's private
vulnerability reporting for this repository:

https://github.com/HeartBtz/pxl/security/advisories/new

Include the affected revision, deployment assumptions, reproduction steps,
impact, and any suggested mitigation. Remove credentials, tokens, private image
content, and personal data from the report.

Maintainers will acknowledge a report when available, investigate it, and
coordinate disclosure after a fix or mitigation is ready. No response-time or
bounty commitment is currently offered.

## Operational Security

PXL operators are responsible for TLS termination, secret storage, database and
object backups, network exposure, reverse-proxy configuration, and timely image
updates. The example Compose stack binds the application to loopback and disables
anonymous uploads and public registration by default.
