# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Authentication and authorization pattern detection.

Scans source files for common auth patterns to determine what
authentication strategies a repository uses.
"""

from __future__ import annotations

import os
import re
from dataclasses import dataclass, field
from pathlib import Path


@dataclass
class AuthDetectionResult:
    """Results of scanning for authentication patterns."""

    patterns: list[str] = field(default_factory=list)
    auth_files: list[str] = field(default_factory=list)
    has_sso: bool = False
    has_mfa: bool = False
    has_rbac: bool = False
    has_api_keys: bool = False
    has_jwt: bool = False
    has_session: bool = False
    has_oauth: bool = False
    provider: str = ""  # "supabase", "nextauth", "passport", "custom", etc.
    findings: list[str] = field(default_factory=list)

    def to_dict(self) -> dict:
        return {
            "patterns": self.patterns,
            "auth_files": self.auth_files[:20],
            "has_sso": self.has_sso,
            "has_mfa": self.has_mfa,
            "has_rbac": self.has_rbac,
            "has_api_keys": self.has_api_keys,
            "has_jwt": self.has_jwt,
            "has_session": self.has_session,
            "has_oauth": self.has_oauth,
            "provider": self.provider,
            "findings": self.findings,
        }


_AUTH_PATTERNS = [
    (re.compile(r"supabase.*auth|createClient.*supabase", re.IGNORECASE), "Supabase Auth"),
    (re.compile(r"next-auth|NextAuth|getServerSession", re.IGNORECASE), "NextAuth"),
    (re.compile(r"passport\.|passport\.authenticate", re.IGNORECASE), "Passport.js"),
    (re.compile(r"flask[_-]login|login_required|LoginManager", re.IGNORECASE), "Flask-Login"),
    (re.compile(r"django\.contrib\.auth|authenticate\(|login\(", re.IGNORECASE), "Django Auth"),
    (re.compile(r"firebase.*auth|signInWith", re.IGNORECASE), "Firebase Auth"),
    (re.compile(r"Auth0|auth0", re.IGNORECASE), "Auth0"),
    (re.compile(r"keycloak|Keycloak", re.IGNORECASE), "Keycloak"),
    (re.compile(r"cognito|CognitoIdentityProvider", re.IGNORECASE), "AWS Cognito"),
    (re.compile(r"OneLogin|onelogin|SAML", re.IGNORECASE), "SAML/OneLogin"),
    (re.compile(r"OIDC|openid|oidc", re.IGNORECASE), "OIDC"),
]

_SSO_PATTERNS = re.compile(r"SAML|SSO|sso|onelogin|OIDC|openid", re.IGNORECASE)
_MFA_PATTERNS = re.compile(r"MFA|mfa|two.factor|2fa|totp|authenticator", re.IGNORECASE)
_RBAC_PATTERNS = re.compile(r"role|permission|isAdmin|is_admin|authorize|hasRole|user_metadata.*role", re.IGNORECASE)
_API_KEY_PATTERNS = re.compile(r"api[_-]key|apiKey|API_KEY|x-api-key|Bearer", re.IGNORECASE)
_JWT_PATTERNS = re.compile(r"jwt|JWT|jsonwebtoken|jose|JWTManager", re.IGNORECASE)
_SESSION_PATTERNS = re.compile(r"session|cookie|express-session|sessionStorage", re.IGNORECASE)
_OAUTH_PATTERNS = re.compile(r"oauth|OAuth|google.*auth|github.*auth|signInWithOAuth", re.IGNORECASE)

_SCAN_EXTENSIONS = {".ts", ".tsx", ".js", ".jsx", ".py", ".go", ".java", ".rb", ".rs", ".php"}


def detect_auth(repo_path: str) -> AuthDetectionResult:
    """Scan a repository for authentication patterns."""
    result = AuthDetectionResult()
    root = Path(repo_path)

    if not root.exists():
        return result

    seen_patterns: set[str] = set()
    auth_files: set[str] = set()

    for dirpath, dirnames, filenames in os.walk(root):
        rel_dir = os.path.relpath(dirpath, root)
        if any(skip in rel_dir.split(os.sep) for skip in [
            "node_modules", ".git", "vendor", ".venv", "venv",
            "__pycache__", ".next", "dist", "build",
        ]):
            continue

        for filename in filenames:
            ext = os.path.splitext(filename)[1].lower()
            if ext not in _SCAN_EXTENSIONS:
                continue

            filepath = os.path.join(dirpath, filename)
            rel_path = os.path.relpath(filepath, root)

            try:
                content = Path(filepath).read_text(errors="ignore")[:50000]  # cap at 50KB
            except OSError:
                continue

            # Check auth provider patterns
            for pattern, name in _AUTH_PATTERNS:
                if pattern.search(content):
                    seen_patterns.add(name)
                    auth_files.add(rel_path)

            # Check specific capability patterns
            if _SSO_PATTERNS.search(content):
                result.has_sso = True
            if _MFA_PATTERNS.search(content):
                result.has_mfa = True
            if _RBAC_PATTERNS.search(content):
                result.has_rbac = True
            if _API_KEY_PATTERNS.search(content):
                result.has_api_keys = True
            if _JWT_PATTERNS.search(content):
                result.has_jwt = True
            if _SESSION_PATTERNS.search(content):
                result.has_session = True
            if _OAUTH_PATTERNS.search(content):
                result.has_oauth = True

    result.patterns = sorted(seen_patterns)
    result.auth_files = sorted(auth_files)[:20]

    # Determine primary provider
    if "Supabase Auth" in seen_patterns:
        result.provider = "supabase"
    elif "NextAuth" in seen_patterns:
        result.provider = "nextauth"
    elif "Passport.js" in seen_patterns:
        result.provider = "passport"
    elif "Firebase Auth" in seen_patterns:
        result.provider = "firebase"
    elif len(seen_patterns) > 0:
        result.provider = list(seen_patterns)[0].lower().replace(" ", "_")
    else:
        result.provider = "none_detected"

    # Generate findings
    if not seen_patterns:
        result.findings.append("No authentication patterns detected in the codebase.")
    if not result.has_mfa:
        result.findings.append("No multi-factor authentication (MFA) patterns detected.")
    if not result.has_rbac:
        result.findings.append("No role-based access control (RBAC) patterns detected.")

    return result
