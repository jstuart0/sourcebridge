package auth

import (
	"context"
	"testing"
	"time"
)

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
