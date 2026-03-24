// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package auth

import (
	"testing"
)

func TestMemoryPersisterLoadUser(t *testing.T) {
	p := MemoryPersister{}
	user, err := p.LoadUser()
	if err != nil {
		t.Fatalf("LoadUser() error: %v", err)
	}
	if user != nil {
		t.Error("MemoryPersister.LoadUser() should return nil")
	}
}

func TestMemoryPersisterSaveUser(t *testing.T) {
	p := MemoryPersister{}
	err := p.SaveUser(&LocalUser{Email: "test@example.com"})
	if err != nil {
		t.Fatalf("SaveUser() error: %v", err)
	}

	// Loading after save should still return nil (no-op persister)
	user, err := p.LoadUser()
	if err != nil {
		t.Fatalf("LoadUser() error: %v", err)
	}
	if user != nil {
		t.Error("MemoryPersister should not persist users")
	}
}

// testPersister is a functional in-memory persister for testing.
type testPersister struct {
	user *LocalUser
}

func (p *testPersister) LoadUser() (*LocalUser, error) {
	return p.user, nil
}

func (p *testPersister) SaveUser(user *LocalUser) error {
	p.user = user
	return nil
}

func TestLocalAuthWithPersister(t *testing.T) {
	persister := &testPersister{}
	mgr := NewJWTManager("test-secret", 60, "")
	la := NewLocalAuth(mgr, persister)

	// Setup should save to persister
	_, err := la.Setup("password123")
	if err != nil {
		t.Fatalf("Setup() error: %v", err)
	}

	if persister.user == nil {
		t.Fatal("persister should have saved user on setup")
	}
	if persister.user.Email != "admin@localhost" {
		t.Errorf("expected admin@localhost, got %s", persister.user.Email)
	}
}

func TestLocalAuthReloadsFromPersister(t *testing.T) {
	// First: setup a user and save to persister
	persister := &testPersister{}
	mgr := NewJWTManager("test-secret", 60, "")
	la := NewLocalAuth(mgr, persister)
	la.Setup("password123")

	// Second: create a new LocalAuth that should load from persister
	la2 := NewLocalAuth(mgr, persister)

	if !la2.IsSetupDone() {
		t.Error("new LocalAuth should detect setup from persister")
	}

	// Login with persisted user should work
	token, err := la2.Login("password123")
	if err != nil {
		t.Fatalf("Login() error after reload: %v", err)
	}
	if token == "" {
		t.Error("expected non-empty token")
	}
}

func TestLocalAuthPasswordChangePersists(t *testing.T) {
	persister := &testPersister{}
	mgr := NewJWTManager("test-secret", 60, "")
	la := NewLocalAuth(mgr, persister)
	la.Setup("password123")

	err := la.ChangePassword("password123", "newpassword456")
	if err != nil {
		t.Fatalf("ChangePassword() error: %v", err)
	}

	// Simulate restart by creating new LocalAuth
	la2 := NewLocalAuth(mgr, persister)

	// Old password should fail
	_, err = la2.Login("password123")
	if err == nil {
		t.Error("old password should fail after change and reload")
	}

	// New password should work
	_, err = la2.Login("newpassword456")
	if err != nil {
		t.Fatalf("new password should work after reload: %v", err)
	}
}

func TestLocalAuthNoPersister(t *testing.T) {
	// Without persister, LocalAuth should work as before
	mgr := NewJWTManager("test-secret", 60, "")
	la := NewLocalAuth(mgr)

	_, err := la.Setup("password123")
	if err != nil {
		t.Fatalf("Setup() error: %v", err)
	}
	if !la.IsSetupDone() {
		t.Error("expected setup done")
	}
}
