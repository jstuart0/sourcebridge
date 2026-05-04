package auth

import (
	"context"
	"testing"
	"time"
)

// TestAPITokenRoleDefaultsToUser verifies that CreateToken stores RoleUser
// when no role is requested.
func TestAPITokenRoleDefaultsToUser(t *testing.T) {
	store := NewAPITokenStore()
	_, record, err := store.CreateToken(context.Background(), CreateTokenInput{
		Name:   "No Role",
		UserID: "user-1",
		Kind:   TokenKindAdminAPI,
	})
	if err != nil {
		t.Fatalf("CreateToken() error: %v", err)
	}
	if record.Role != RoleUser {
		t.Fatalf("expected role %q, got %q", RoleUser, record.Role)
	}
}

// TestAPITokenRoleAdminRoundTrips verifies that a token created with RoleAdmin
// stores and retrieves that role.
func TestAPITokenRoleAdminRoundTrips(t *testing.T) {
	store := NewAPITokenStore()
	rawToken, record, err := store.CreateToken(context.Background(), CreateTokenInput{
		Name:   "Admin Token",
		UserID: "user-1",
		Kind:   TokenKindAdminAPI,
		Role:   RoleAdmin,
	})
	if err != nil {
		t.Fatalf("CreateToken() error: %v", err)
	}
	if record.Role != RoleAdmin {
		t.Fatalf("expected role %q after create, got %q", RoleAdmin, record.Role)
	}

	// Round-trip through ValidateToken.
	validated, err := store.ValidateToken(context.Background(), rawToken)
	if err != nil {
		t.Fatalf("ValidateToken() error: %v", err)
	}
	if validated == nil {
		t.Fatal("expected token to validate")
	}
	if validated.Role != RoleAdmin {
		t.Fatalf("expected role %q after validate, got %q", RoleAdmin, validated.Role)
	}
}

// TestAPITokenRoleUserRoundTrips verifies that RoleUser survives a
// ValidateToken round-trip.
func TestAPITokenRoleUserRoundTrips(t *testing.T) {
	store := NewAPITokenStore()
	rawToken, record, err := store.CreateToken(context.Background(), CreateTokenInput{
		Name:   "User Token",
		UserID: "user-2",
		Kind:   TokenKindAdminAPI,
		Role:   RoleUser,
	})
	if err != nil {
		t.Fatalf("CreateToken() error: %v", err)
	}
	if record.Role != RoleUser {
		t.Fatalf("expected role %q after create, got %q", RoleUser, record.Role)
	}

	validated, err := store.ValidateToken(context.Background(), rawToken)
	if err != nil {
		t.Fatalf("ValidateToken() error: %v", err)
	}
	if validated == nil {
		t.Fatal("expected token to validate")
	}
	if validated.Role != RoleUser {
		t.Fatalf("expected role %q after validate, got %q", RoleUser, validated.Role)
	}
}

func TestMemoryAPITokenStoreValidateAndRevoke(t *testing.T) {
	store := NewAPITokenStore()
	rawToken, record, err := store.CreateToken(context.Background(), CreateTokenInput{
		Name:       "Test IDE Session",
		UserID:     "user-1",
		Kind:       TokenKindIDESession,
		ClientType: "desktop_ide",
		AuthMethod: AuthMethodLocalPassword,
	})
	if err != nil {
		t.Fatalf("CreateToken() error: %v", err)
	}
	if rawToken == "" {
		t.Fatal("expected raw token")
	}
	if record.Kind != TokenKindIDESession {
		t.Fatalf("expected ide_session kind, got %s", record.Kind)
	}

	validated, err := store.ValidateToken(context.Background(), rawToken)
	if err != nil {
		t.Fatalf("ValidateToken() error: %v", err)
	}
	if validated == nil {
		t.Fatal("expected token to validate")
	}
	if validated.LastUsedAt == nil {
		t.Fatal("ValidateToken() should stamp last_used_at")
	}

	ok, err := store.RevokeToken(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("RevokeToken() error: %v", err)
	}
	if !ok {
		t.Fatal("expected revoke to succeed")
	}

	validated, err = store.ValidateToken(context.Background(), rawToken)
	if err != nil {
		t.Fatalf("ValidateToken() after revoke error: %v", err)
	}
	if validated != nil {
		t.Fatal("revoked token should not validate")
	}
}

func TestMemoryAPITokenStoreExpiredTokenDoesNotValidate(t *testing.T) {
	store := NewAPITokenStore()
	expiry := time.Now().Add(-1 * time.Hour)
	rawToken, _, err := store.CreateToken(context.Background(), CreateTokenInput{
		Name:       "Expired Token",
		UserID:     "user-1",
		Kind:       TokenKindIDESession,
		ClientType: "desktop_ide",
		AuthMethod: AuthMethodLocalPassword,
		ExpiresAt:  &expiry,
	})
	if err != nil {
		t.Fatalf("CreateToken() error: %v", err)
	}

	validated, err := store.ValidateToken(context.Background(), rawToken)
	if err != nil {
		t.Fatalf("ValidateToken() error: %v", err)
	}
	if validated != nil {
		t.Fatal("expired token should not validate")
	}
}

func TestMemoryAPITokenStoreWrongTokenDoesNotValidate(t *testing.T) {
	store := NewAPITokenStore()
	_, _, err := store.CreateToken(context.Background(), CreateTokenInput{
		Name:       "Real Token",
		UserID:     "user-1",
		Kind:       TokenKindIDESession,
		ClientType: "desktop_ide",
		AuthMethod: AuthMethodLocalPassword,
	})
	if err != nil {
		t.Fatalf("CreateToken() error: %v", err)
	}

	validated, err := store.ValidateToken(context.Background(), "ca_fake_token_that_does_not_exist")
	if err != nil {
		t.Fatalf("ValidateToken() error: %v", err)
	}
	if validated != nil {
		t.Fatal("wrong token should not validate")
	}
}

func TestMemoryOIDCStateStoreConsumesOnlyOnce(t *testing.T) {
	store := NewMemoryOIDCStateStore()
	if err := store.SaveState(context.Background(), "state-1", time.Now().Add(5*time.Minute)); err != nil {
		t.Fatalf("SaveState() error: %v", err)
	}

	ok, err := store.ConsumeState(context.Background(), "state-1")
	if err != nil {
		t.Fatalf("ConsumeState() error: %v", err)
	}
	if !ok {
		t.Fatal("expected first consume to succeed")
	}

	ok, err = store.ConsumeState(context.Background(), "state-1")
	if err != nil {
		t.Fatalf("second ConsumeState() error: %v", err)
	}
	if ok {
		t.Fatal("expected second consume to fail")
	}
}
