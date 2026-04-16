# Security Policy

## Supported Versions

| Version | Supported          |
| ------- | ------------------ |
| 0.5.x   | Yes                |
| < 0.5   | No                 |

## Reporting a Vulnerability

If you discover a security vulnerability in SourceBridge, please report it responsibly.

**Do not open a public GitHub issue for security vulnerabilities.**

Instead, please email **security@sourcebridge.ai** with:

- A description of the vulnerability
- Steps to reproduce
- The potential impact
- Any suggested fixes (optional)

We will acknowledge your report within 48 hours and aim to provide a fix or mitigation plan within 7 days for critical issues.

## Scope

This policy covers:

- The SourceBridge API server (Go)
- The SourceBridge web dashboard (Next.js)
- The SourceBridge AI worker (Python)
- Docker images published under the `sourcebridge` Docker Hub organization
- Container images published to `ghcr.io/sourcebridge-ai`

## Security Best Practices for Deployments

- Always set unique, strong values for `SOURCEBRIDGE_SECURITY_JWT_SECRET` and `SOURCEBRIDGE_GRPC_AUTH_SECRET` in production
- Do not expose SurrealDB ports to the public internet
- Use HTTPS (TLS termination) in front of the API and web servers
- Keep your LLM API keys in environment variables or secrets management, never in config files committed to version control
- Review the [Going to Production](docs/going-to-production.md) guide before deploying

## Recognition

We appreciate the security research community's efforts in helping keep SourceBridge safe. Reporters of valid vulnerabilities will be credited in release notes (unless they prefer to remain anonymous).
