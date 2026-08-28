# Security Policy

Sentra encrypts everything client-side before it reaches storage; the
security model is documented in [docs/threat-model.md](docs/threat-model.md).
If you believe you have found a vulnerability, please report it privately —
do not open a public issue.

## Reporting

Use GitHub's private vulnerability reporting on this repository
(**Security → Report a vulnerability**). Reports are acknowledged within a
week. Please include a reproduction or a pointer into the code, and what an
attacker gains.

## Scope

Most interesting: anything that breaks the invariants in the threat model —
plaintext or key material reaching the bucket, nonce reuse, a crafted
manifest escaping the restore root, a repository lock that can be stolen, or
secrets landing in `sentra.yaml`, logs, or recovery kits.

Out of scope: attacks requiring a compromised local machine (the threat
model assumes the client is trusted), and denial-of-service against your own
bucket.

## Supported versions

The latest release is supported. There is no LTS branch.
