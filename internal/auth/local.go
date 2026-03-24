// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"

	"golang.org/x/crypto/bcrypt"
)

// LocalUser represents the single OSS user.
type LocalUser struct {
	ID           string `json:"id"`
	Email        string `json:"email"`
	Name         string `json:"name"`
	PasswordHash string `json:"password_hash"`
}

// LocalAuth handles single-user authentication for OSS mode.
type LocalAuth struct {
	jwtManager *JWTManager
	persister  AuthPersister
	user       *LocalUser
	setupDone  bool
}

// NewLocalAuth creates a new local auth handler.
// If persister is nil, a MemoryPersister is used (no persistence across restarts).
func NewLocalAuth(jwtManager *JWTManager, persister ...AuthPersister) *LocalAuth {
	var p AuthPersister = MemoryPersister{}
	if len(persister) > 0 && persister[0] != nil {
		p = persister[0]
	}

	la := &LocalAuth{
		jwtManager: jwtManager,
		persister:  p,
	}

	// Attempt to load persisted user
	if user, err := p.LoadUser(); err != nil {
		slog.Warn("failed to load persisted auth user", "error", err)
	} else if user != nil {
		la.user = user
		la.setupDone = true
		slog.Info("loaded persisted admin user", "email", user.Email)
	}

	return la
}

// IsSetupDone returns whether initial setup has been completed.
func (a *LocalAuth) IsSetupDone() bool {
	return a.setupDone
}

// Setup creates the initial user with a password. Called on first launch.
func (a *LocalAuth) Setup(password string) (*LocalUser, error) {
	if a.setupDone {
		return nil, fmt.Errorf("setup already completed")
	}

	if len(password) < 8 {
		return nil, fmt.Errorf("password must be at least 8 characters")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("failed to hash password: %w", err)
	}

	id, err := generateID()
	if err != nil {
		return nil, fmt.Errorf("failed to generate user ID: %w", err)
	}

	a.user = &LocalUser{
		ID:           id,
		Email:        "admin@localhost",
		Name:         "Admin",
		PasswordHash: string(hash),
	}
	a.setupDone = true

	// Persist the new user
	if err := a.persister.SaveUser(a.user); err != nil {
		slog.Warn("failed to persist user after setup", "error", err)
	}

	return a.user, nil
}

// Login authenticates with password and returns a JWT.
func (a *LocalAuth) Login(password string) (string, error) {
	if !a.setupDone || a.user == nil {
		return "", fmt.Errorf("setup not completed")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(a.user.PasswordHash), []byte(password)); err != nil {
		return "", fmt.Errorf("invalid credentials")
	}

	return a.jwtManager.GenerateToken(a.user.ID, a.user.Email, "", "")
}

// ChangePassword changes the user's password.
func (a *LocalAuth) ChangePassword(oldPassword, newPassword string) error {
	if !a.setupDone || a.user == nil {
		return fmt.Errorf("setup not completed")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(a.user.PasswordHash), []byte(oldPassword)); err != nil {
		return fmt.Errorf("invalid current password")
	}

	if len(newPassword) < 8 {
		return fmt.Errorf("new password must be at least 8 characters")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("failed to hash new password: %w", err)
	}

	a.user.PasswordHash = string(hash)

	// Persist the updated hash
	if err := a.persister.SaveUser(a.user); err != nil {
		slog.Warn("failed to persist user after password change", "error", err)
	}

	return nil
}

// GetUser returns the current user, or nil if not set up.
func (a *LocalAuth) GetUser() *LocalUser {
	return a.user
}

func generateID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
