// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims represents JWT token claims.
type Claims struct {
	jwt.RegisteredClaims
	UserID string `json:"uid"`
	Email  string `json:"email"`
	OrgID  string `json:"org,omitempty"`
	Role   string `json:"role,omitempty"`
}

// JWTManager handles JWT token creation and validation.
type JWTManager struct {
	secret  []byte
	ttl     time.Duration
	issuer  string
	edition string
}

// NewJWTManager creates a new JWT manager.
// The edition parameter (e.g. "enterprise") namespaces the JWT issuer and
// cookie names so that tokens and sessions from different editions cannot
// cross-validate.
func NewJWTManager(secret string, ttlMinutes int, edition string) *JWTManager {
	issuer := "sourcebridge"
	if edition != "" && edition != "oss" {
		issuer = "sourcebridge-" + edition
	}
	return &JWTManager{
		secret:  []byte(secret),
		ttl:     time.Duration(ttlMinutes) * time.Minute,
		issuer:  issuer,
		edition: edition,
	}
}

// SessionCookieName returns the edition-scoped session cookie name.
func (j *JWTManager) SessionCookieName() string {
	if j.edition != "" && j.edition != "oss" {
		return "sourcebridge_" + j.edition + "_session"
	}
	return "sourcebridge_session"
}

// CSRFCookieName returns the edition-scoped CSRF cookie name.
func (j *JWTManager) CSRFCookieName() string {
	if j.edition != "" && j.edition != "oss" {
		return "sourcebridge_" + j.edition + "_csrf"
	}
	return "sourcebridge_csrf"
}

// GenerateToken creates a new JWT token for a user.
func (j *JWTManager) GenerateToken(userID, email, orgID, role string) (string, error) {
	now := time.Now()
	claims := &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    j.issuer,
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(j.ttl)),
			NotBefore: jwt.NewNumericDate(now),
		},
		UserID: userID,
		Email:  email,
		OrgID:  orgID,
		Role:   role,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(j.secret)
}

// ValidateToken parses and validates a JWT token.
func (j *JWTManager) ValidateToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return j.secret, nil
	})
	if err != nil {
		return nil, fmt.Errorf("invalid token: %w", err)
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}

	return claims, nil
}
