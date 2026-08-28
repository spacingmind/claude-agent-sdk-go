# Security Policy

`claude-agent-sdk-go` is a small, early-stage project without a dedicated
security team. We still take vulnerability reports seriously and will
respond as promptly as we can.

## Reporting a vulnerability

**Please do not open a public GitHub issue for security-sensitive
findings.** Instead, use GitHub's private vulnerability reporting:

<https://github.com/spacingmind/claude-agent-sdk-go/security/advisories/new>

This opens a private advisory visible only to the maintainers, where you
can describe the issue, its impact, and any reproduction steps. We'll
follow up there.

## Scope

This applies to vulnerabilities in this package itself (e.g. the wire
protocol handling, control-request/response processing, permission
handling). Issues in the `claude` CLI it drives, or in Anthropic's
services, should be reported to Anthropic directly.

## Supported versions

This project is pre-`v1`; there is no long-term-support branch. Security
fixes land on the latest release.
