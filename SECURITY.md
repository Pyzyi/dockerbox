# Security policy

Dockerbox is a security boundary between AI agents and your server. Reports are
taken seriously.

## Supported versions

Only the latest minor release receives security fixes.

## Reporting a vulnerability

Please **do not open a public issue**. Use GitHub's private vulnerability
reporting: *Security → Report a vulnerability* in this repository.

Include:

* affected version / commit,
* a description and, if possible, a proof of concept,
* the impact you expect (e.g. "agent can reach the host network").

You will receive an acknowledgement within 7 days. Fixes are released as soon as
possible and credited in the changelog unless you prefer otherwise.

## Scope

In scope: container escape or host access from an agent box, network isolation
bypasses (host, LAN, other boxes), authentication/CSRF issues in the dashboard,
broker policy bypasses.

Out of scope: vulnerabilities in the Linux kernel or Docker Engine themselves
(please report upstream), software installed by users inside their boxes.

See [docs/security/threat-model.md](docs/security/threat-model.md) for the
assumptions and accepted residual risks.
