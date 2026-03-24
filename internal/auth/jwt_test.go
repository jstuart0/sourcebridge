package auth

import (
	"testing"
	"time"
)

func TestJWTGenerateAndValidate(t *testing.T) {
	mgr := NewJWTManager("test-secret-key", 60, "")

	token, err := mgr.GenerateToken("user-123", "test@example.com", "", "")
	if err != nil {
		t.Fatalf("GenerateToken() error: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty token")
	}

	claims, err := mgr.ValidateToken(token)
	if err != nil {
		t.Fatalf("ValidateToken() error: %v", err)
	}
	if claims.UserID != "user-123" {
		t.Errorf("expected user-123, got %s", claims.UserID)
	}
	if claims.Email != "test@example.com" {
		t.Errorf("expected test@example.com, got %s", claims.Email)
	}
}

func TestJWTWithOrgAndRole(t *testing.T) {
	mgr := NewJWTManager("test-secret", 60, "")

	token, err := mgr.GenerateToken("user-1", "admin@org.com", "org-abc", "admin")
	if err != nil {
		t.Fatalf("GenerateToken() error: %v", err)
	}

	claims, err := mgr.ValidateToken(token)
	if err != nil {
		t.Fatalf("ValidateToken() error: %v", err)
	}
	if claims.OrgID != "org-abc" {
		t.Errorf("expected org-abc, got %s", claims.OrgID)
	}
	if claims.Role != "admin" {
		t.Errorf("expected admin, got %s", claims.Role)
	}
}

func TestJWTInvalidToken(t *testing.T) {
	mgr := NewJWTManager("secret", 60, "")

	_, err := mgr.ValidateToken("invalid-token-string")
	if err == nil {
		t.Error("expected error for invalid token")
	}
}

func TestJWTDifferentSecret(t *testing.T) {
	mgr1 := NewJWTManager("secret-1", 60, "")
	mgr2 := NewJWTManager("secret-2", 60, "")

	token, _ := mgr1.GenerateToken("user-1", "test@test.com", "", "")
	_, err := mgr2.ValidateToken(token)
	if err == nil {
		t.Error("expected error for wrong secret")
	}
}

func TestJWTExpired(t *testing.T) {
	// Create manager with 0-minute TTL (expires immediately)
	mgr := &JWTManager{
		secret: []byte("secret"),
		ttl:    -1 * time.Second,
		issuer: "sourcebridge",
	}

	token, err := mgr.GenerateToken("user-1", "test@test.com", "", "")
	if err != nil {
		t.Fatalf("GenerateToken() error: %v", err)
	}

	_, err = mgr.ValidateToken(token)
	if err == nil {
		t.Error("expected error for expired token")
	}
}
