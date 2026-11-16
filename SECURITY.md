# Security Policy

## Security Model (`127.0.0.1` Loopback Boundary)

`repotier` is a local developer tool and localhost HTTP daemon:
- **Loopback-Only Binding (`WEB-SEC-001`):** The daemon binds strictly to `127.0.0.1:<port>` and never listens on `0.0.0.0` or external network interfaces.
- **Cross-Origin Request Protection (`WEB-SEC-002`):** Every non-`GET`/`HEAD` request is guarded by `localhostGuard`, which rejects requests with a non-empty `Origin` header unless it matches `http://127.0.0.1:<port>` or `http://localhost:<port>` (`403 Forbidden`).
- **Subprocess Isolation (`GITOPS-EXEC-001`):** All Git operations execute via `os/exec` argument vectors (`git -C <path> ...`) without shell interpolation and are bounded by a `10s` execution timeout.

## Supported Versions

`repotier` follows CalVer (`YYYY.MM.N`). Only the latest published release is actively supported with security updates:

| Version | Supported |
|---|---|
| Latest (`YYYY.MM.N`) | ✅ |
| Older releases | ❌ (run `repotier update` to upgrade) |

## Reporting a Vulnerability

Please **do not** report security vulnerabilities through public GitHub issues.

Instead, report vulnerabilities privately via **[GitHub Private Vulnerability Reporting](https://github.com/BaankeyBihari/repotier/security/advisories/new)** on this repository. Include:
1. Affected version (`repotier` daemon info or release tag).
2. Minimal reproduction steps or proof-of-concept request.
3. Impact assessment (e.g., cross-origin bypass, path traversal, command injection).
