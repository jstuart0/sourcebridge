"""Tests for the authentication module.

REQ-010: Verify password hashing
REQ-011: Verify session management
REQ-012: Verify registration
"""

import pytest
from auth import AuthService


@pytest.fixture
def auth_service():
    return AuthService(secret_key="test-secret-key")


def test_req_010_password_hashing(auth_service):
    """REQ-010: Passwords are hashed and verifiable."""
    password = "secure_password_123"
    hashed = auth_service.hash_password(password)
    assert hashed != password
    assert auth_service.verify_password(password, hashed)
    assert not auth_service.verify_password("wrong_password", hashed)


def test_req_012_register_user(auth_service):
    """REQ-012: Users can register with unique email."""
    user = auth_service.register_user("test@example.com", "Test User", "password123")
    assert user.email == "test@example.com"
    assert user.name == "Test User"
    assert user.password_hash != "password123"


def test_req_012_duplicate_email(auth_service):
    """REQ-012: Duplicate emails are rejected."""
    auth_service.register_user("test@example.com", "User 1", "pass1")
    with pytest.raises(ValueError, match="already exists"):
        auth_service.register_user("test@example.com", "User 2", "pass2")


def test_req_011_login_creates_session(auth_service):
    """REQ-011: Successful login creates a session with expiry."""
    auth_service.register_user("test@example.com", "Test", "password123")
    session = auth_service.login("test@example.com", "password123")
    assert session.token
    assert session.expires_at > session.created_at


def test_req_011_invalid_login(auth_service):
    """REQ-011: Invalid credentials raise error."""
    auth_service.register_user("test@example.com", "Test", "password123")
    with pytest.raises(ValueError, match="Invalid credentials"):
        auth_service.login("test@example.com", "wrong_password")


def test_req_013_validate_session(auth_service):
    """REQ-013: Valid session tokens return user."""
    auth_service.register_user("test@example.com", "Test", "password123")
    session = auth_service.login("test@example.com", "password123")
    user = auth_service.validate_session(session.token)
    assert user is not None
    assert user.email == "test@example.com"


def test_req_013_invalid_token(auth_service):
    """REQ-013: Invalid tokens return None."""
    user = auth_service.validate_session("invalid-token")
    assert user is None
