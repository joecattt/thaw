# Security Policy

## Reporting a vulnerability

Please report vulnerabilities privately via
[GitHub Security Advisories](https://github.com/joecattt/thaw/security/advisories/new)
— not in a public issue.

## Scope

Thaw captures terminal state (commands, history, environment) to local disk,
so anything that leaks what it promises to protect is in scope. In particular:

- **Credential-scrub bypasses** — any input (command, history line, env var,
  captured output) where a secret survives redaction and reaches disk.
- File permissions on captured data (snapshots, command log, exported HTML).
- The `thaw allow` / `.thaw.toml` trust gate — any way a cloned repo can run
  commands without explicit approval.
- The HMAC audit chain — undetected tampering with snapshot integrity.

Out of scope: issues requiring an attacker who already has your local user
account (thaw's data is only as private as your home directory), and
vulnerabilities in third-party feeds the dashboard's opt-in news rail reads.

## Response

This is a volunteer-maintained project. Expect an acknowledgment within a
week and a fix or mitigation plan within 30 days for confirmed issues.
Please allow a fix to ship before public disclosure.
