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

func TestLocalAuthLoginIssuesAdminRole(t *testing.T) {
	mgr := NewJWTManager("test-secret", 60, "")
	la := NewLocalAuth(mgr)

	if _, err := la.Setup("password123"); err != nil {
		t.Fatalf("Setup() error: %v", err)
	}

	token, err := la.Login("password123")
	if err != nil {
		t.Fatalf("Login() error: %v", err)
	}

	claims, err := mgr.ValidateToken(token)
	if err != nil {
		t.Fatalf("ValidateToken() error: %v", err)
	}
	if claims.Role != RoleAdmin {
		t.Errorf("expected role %q, got %q — bootstrap admin JWT must carry admin role", RoleAdmin, claims.Role)
	}
}

// CA-215: password min-length configurable via LocalAuthOptions.

func TestLocalAuthOptions_DefaultMinLength(t *testing.T) {
	mgr := NewJWTManager("test-secret", 60, "")
	la := NewLocalAuth(mgr)
	if la.PasswordMinLength() != 8 {
		t.Errorf("default PasswordMinLength: want 8, got %d", la.PasswordMinLength())
	}
}

func TestLocalAuthOptions_CustomMinLength(t *testing.T) {
	mgr := NewJWTManager("test-secret", 60, "")
	la := NewLocalAuthWithOptions(mgr, LocalAuthOptions{PasswordMinLength: 12})
	if la.PasswordMinLength() != 12 {
		t.Errorf("custom PasswordMinLength: want 12, got %d", la.PasswordMinLength())
	}
}

func TestLocalAuthOptions_ZeroFallsBackToDefault(t *testing.T) {
	mgr := NewJWTManager("test-secret", 60, "")
	la := NewLocalAuthWithOptions(mgr, LocalAuthOptions{PasswordMinLength: 0})
	if la.PasswordMinLength() != 8 {
		t.Errorf("zero PasswordMinLength: want 8 (default), got %d", la.PasswordMinLength())
	}
}

func TestLocalAuthSetup_RejectsShortPasswordWithCustomMin(t *testing.T) {
	mgr := NewJWTManager("test-secret", 60, "")
	la := NewLocalAuthWithOptions(mgr, LocalAuthOptions{PasswordMinLength: 12})

	// 11 chars — below min of 12
	_, err := la.Setup("password123")
	if err == nil {
		t.Error("expected error for password shorter than custom min")
	}
}

func TestLocalAuthSetup_AcceptsPasswordAtCustomMin(t *testing.T) {
	mgr := NewJWTManager("test-secret", 60, "")
	la := NewLocalAuthWithOptions(mgr, LocalAuthOptions{PasswordMinLength: 12})

	// exactly 12 chars
	_, err := la.Setup("passwordTwelv")
	if err != nil {
		t.Errorf("unexpected error for password at custom min: %v", err)
	}
}

func TestLocalAuthChangePassword_RejectsShortPasswordWithCustomMin(t *testing.T) {
	mgr := NewJWTManager("test-secret", 60, "")
	la := NewLocalAuthWithOptions(mgr, LocalAuthOptions{PasswordMinLength: 12})
	if _, err := la.Setup("passwordTwelv"); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// new password only 8 chars — below min of 12
	err := la.ChangePassword("passwordTwelv", "short123")
	if err == nil {
		t.Error("expected error for new password shorter than custom min")
	}
}
