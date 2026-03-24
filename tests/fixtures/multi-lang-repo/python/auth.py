"""Authentication module for user management.

REQ-010: User authentication with secure password handling
REQ-011: Session management with JWT tokens
"""

from __future__ import annotations

import hashlib
import hmac
import secrets
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
from typing import Optional


@dataclass
class User:
    """Represents an authenticated user."""

    id: str
    email: str
    name: str
    password_hash: str
    created_at: datetime = field(default_factory=lambda: datetime.now(timezone.utc))
    is_active: bool = True


@dataclass
class Session:
    """Represents an active user session."""

    token: str
    user_id: str
    expires_at: datetime
    created_at: datetime = field(default_factory=lambda: datetime.now(timezone.utc))


class AuthService:
    """Handles user authentication and session management."""

    def __init__(self, secret_key: str, token_ttl_hours: int = 24) -> None:
        self.secret_key = secret_key
        self.token_ttl = timedelta(hours=token_ttl_hours)
        self._users: dict[str, User] = {}
        self._sessions: dict[str, Session] = {}

    def hash_password(self, password: str) -> str:
        """Hash a password using HMAC-SHA256.

        REQ-010: Passwords must be hashed before storage
        """
        salt = secrets.token_hex(16)
        h = hmac.new(
            self.secret_key.encode(),
            (salt + password).encode(),
            hashlib.sha256,
        ).hexdigest()
        return f"{salt}:{h}"

    def verify_password(self, password: str, password_hash: str) -> bool:
        """Verify a password against its hash."""
        salt, expected_hash = password_hash.split(":")
        actual_hash = hmac.new(
            self.secret_key.encode(),
            (salt + password).encode(),
            hashlib.sha256,
        ).hexdigest()
        return hmac.compare_digest(actual_hash, expected_hash)

    def register_user(self, email: str, name: str, password: str) -> User:
        """Register a new user.

        REQ-012: User registration with email uniqueness
        """
        if email in self._users:
            raise ValueError(f"User with email {email} already exists")

        user = User(
            id=secrets.token_urlsafe(16),
            email=email,
            name=name,
            password_hash=self.hash_password(password),
        )
        self._users[email] = user
        return user

    def login(self, email: str, password: str) -> Session:
        """Authenticate a user and create a session.

        REQ-011: Session tokens must expire after configured TTL
        """
        user = self._users.get(email)
        if not user or not user.is_active:
            raise ValueError("Invalid credentials")

        if not self.verify_password(password, user.password_hash):
            raise ValueError("Invalid credentials")

        session = Session(
            token=secrets.token_urlsafe(32),
            user_id=user.id,
            expires_at=datetime.now(timezone.utc) + self.token_ttl,
        )
        self._sessions[session.token] = session
        return session

    def validate_session(self, token: str) -> Optional[User]:
        """Validate a session token and return the user.

        REQ-013: Session validation on every request
        """
        session = self._sessions.get(token)
        if not session:
            return None

        if datetime.now(timezone.utc) > session.expires_at:
            del self._sessions[token]
            return None

        for user in self._users.values():
            if user.id == session.user_id:
                return user
        return None

    def logout(self, token: str) -> None:
        """Invalidate a session."""
        self._sessions.pop(token, None)
