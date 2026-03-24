package auth

import (
	"testing"
)

func TestLocalAuthSetup(t *testing.T) {
	mgr := NewJWTManager("test-secret", 60, "")
	auth := NewLocalAuth(mgr)

	if auth.IsSetupDone() {
		t.Error("expected setup not done initially")
	}

	user, err := auth.Setup("password123")
	if err != nil {
		t.Fatalf("Setup() error: %v", err)
	}
	if user.Email != "admin@localhost" {
		t.Errorf("expected admin@localhost, got %s", user.Email)
	}
	if !auth.IsSetupDone() {
		t.Error("expected setup done after Setup()")
	}
}

func TestLocalAuthSetupShortPassword(t *testing.T) {
	mgr := NewJWTManager("test-secret", 60, "")
	auth := NewLocalAuth(mgr)

	_, err := auth.Setup("short")
	if err == nil {
		t.Error("expected error for short password")
	}
}

func TestLocalAuthSetupTwice(t *testing.T) {
	mgr := NewJWTManager("test-secret", 60, "")
	auth := NewLocalAuth(mgr)

	auth.Setup("password123")
	_, err := auth.Setup("password456")
	if err == nil {
		t.Error("expected error for double setup")
	}
}

func TestLocalAuthLogin(t *testing.T) {
	mgr := NewJWTManager("test-secret", 60, "")
	auth := NewLocalAuth(mgr)

	auth.Setup("password123")

	token, err := auth.Login("password123")
	if err != nil {
		t.Fatalf("Login() error: %v", err)
	}
	if token == "" {
		t.Error("expected non-empty token")
	}

	// Validate the returned token
	claims, err := mgr.ValidateToken(token)
	if err != nil {
		t.Fatalf("ValidateToken() error: %v", err)
	}
	if claims.Email != "admin@localhost" {
		t.Errorf("expected admin@localhost, got %s", claims.Email)
	}
}

func TestLocalAuthLoginWrongPassword(t *testing.T) {
	mgr := NewJWTManager("test-secret", 60, "")
	auth := NewLocalAuth(mgr)

	auth.Setup("password123")

	_, err := auth.Login("wrong-password")
	if err == nil {
		t.Error("expected error for wrong password")
	}
}

func TestLocalAuthLoginBeforeSetup(t *testing.T) {
	mgr := NewJWTManager("test-secret", 60, "")
	auth := NewLocalAuth(mgr)

	_, err := auth.Login("password123")
	if err == nil {
		t.Error("expected error for login before setup")
	}
}

func TestLocalAuthChangePassword(t *testing.T) {
	mgr := NewJWTManager("test-secret", 60, "")
	auth := NewLocalAuth(mgr)

	auth.Setup("password123")

	err := auth.ChangePassword("password123", "newpassword456")
	if err != nil {
		t.Fatalf("ChangePassword() error: %v", err)
	}

	// Old password should fail
	_, err = auth.Login("password123")
	if err == nil {
		t.Error("expected error for old password after change")
	}

	// New password should work
	token, err := auth.Login("newpassword456")
	if err != nil {
		t.Fatalf("Login() with new password error: %v", err)
	}
	if token == "" {
		t.Error("expected non-empty token")
	}
}

func TestLocalAuthChangePasswordWrongOld(t *testing.T) {
	mgr := NewJWTManager("test-secret", 60, "")
	auth := NewLocalAuth(mgr)

	auth.Setup("password123")

	err := auth.ChangePassword("wrong-old", "newpassword456")
	if err == nil {
		t.Error("expected error for wrong old password")
	}
}
