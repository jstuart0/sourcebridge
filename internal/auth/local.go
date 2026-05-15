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

// LocalAuthOptions configures optional behavior for LocalAuth.
type LocalAuthOptions struct {
	// PasswordMinLength is the minimum accepted password length for Setup and
	// ChangePassword. Zero is treated as the default of 8.
	PasswordMinLength int
}

// passwordMinLength returns the effective minimum, applying the default of 8
// when the configured value is zero.
func (o LocalAuthOptions) passwordMinLength() int {
	if o.PasswordMinLength <= 0 {
		return 8
	}
	return o.PasswordMinLength
}

// LocalAuth handles single-user authentication for OSS mode.
type LocalAuth struct {
	jwtManager *JWTManager
	persister  AuthPersister
	opts       LocalAuthOptions
	user       *LocalUser
	setupDone  bool
}

// NewLocalAuth creates a new local auth handler with default options.
// If persister is nil, a MemoryPersister is used (no persistence across restarts).
func NewLocalAuth(jwtManager *JWTManager, persister ...AuthPersister) *LocalAuth {
	return NewLocalAuthWithOptions(jwtManager, LocalAuthOptions{PasswordMinLength: 8}, persister...)
}

// NewLocalAuthWithOptions creates a new local auth handler with explicit options.
// If persister is nil, a MemoryPersister is used (no persistence across restarts).
func NewLocalAuthWithOptions(jwtManager *JWTManager, opts LocalAuthOptions, persister ...AuthPersister) *LocalAuth {
	var p AuthPersister = MemoryPersister{}
	if len(persister) > 0 && persister[0] != nil {
		p = persister[0]
	}

	la := &LocalAuth{
		jwtManager: jwtManager,
		persister:  p,
		opts:       opts,
	}

	// Attempt to load persisted user
	if user, err := p.LoadUser(); err != nil {
		slog.Warn("failed to load persisted auth user", "error", err)
	} else if user != nil {
		la.user = user
		la.setupDone = true
		// CA-340: email is PII — log at Debug to avoid emitting it in production
		// INFO-level log streams. The user.ID is the stable identifier for correlation.
		slog.Debug("loaded persisted admin user", "user_id", user.ID)
		slog.Info("local_auth_setup_loaded", "user_id", user.ID)
	}

	return la
}

// PasswordMinLength returns the configured minimum password length.
func (a *LocalAuth) PasswordMinLength() int {
	return a.opts.passwordMinLength()
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

	if len(password) < a.opts.passwordMinLength() {
		return nil, fmt.Errorf("password must be at least %d characters", a.opts.passwordMinLength())
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

	return a.jwtManager.GenerateToken(a.user.ID, a.user.Email, "", RoleAdmin)
}

// ChangePassword changes the user's password.
func (a *LocalAuth) ChangePassword(oldPassword, newPassword string) error {
	if !a.setupDone || a.user == nil {
		return fmt.Errorf("setup not completed")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(a.user.PasswordHash), []byte(oldPassword)); err != nil {
		return fmt.Errorf("invalid current password")
	}

	if len(newPassword) < a.opts.passwordMinLength() {
		return fmt.Errorf("new password must be at least %d characters", a.opts.passwordMinLength())
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
