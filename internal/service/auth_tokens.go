package service

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/golang-jwt/jwt/v5"
)

func (s *authService) ValidateToken(tokenString string) (*models.User, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(s.jwtConfig.Secret), nil
	})

	if err != nil {
		return nil, err
	}

	if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
		// Safe type assertion with validation
		userIDFloat, ok := claims["user_id"].(float64)
		if !ok {
			return nil, errors.New("invalid user_id in token claims")
		}

		// The session generation is checked for shape before any conversion
		// or database read; an absent claim reads as 0.
		claimedVersion, err := accessTokenVersion(claims)
		if err != nil {
			return nil, err
		}

		userID := uint(userIDFloat)
		user, err := s.userRepo.GetByID(userID)
		if err != nil {
			return nil, err
		}
		if !user.IsActive {
			utils.Logger.WithField("user_id", user.ID).
				Warn("Rejected bearer token of a deactivated user")
			return nil, errors.New("user account is deactivated")
		}
		// A logout, password change or password reset since the token was
		// signed has incremented the stored counter (#236).
		if err := checkAccessTokenVersion(claimedVersion, user.TokenVersion); err != nil {
			utils.Logger.WithField("user_id", user.ID).WithField("operation", "ValidateToken").
				Warn("Rejected access token of an ended session")
			return nil, err
		}
		return user, nil
	}

	return nil, errors.New("invalid token")
}

// GenerateJWT signs the access token of a sign-in: a password login, a
// registration, a single sign-on session or the renewed token of a password
// change. It carries auth_time, the sign-in time (#532).
func (s *authService) GenerateJWT(user *models.User) (string, error) {
	return s.signJWT(user, true)
}

// signJWT signs an access token for user. With signIn the token carries
// auth_time = now (Unix seconds); without it (a refresh rotation) it carries
// none.
func (s *authService) signJWT(user *models.User, signIn bool) (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"user_id": user.ID,
		"email":   user.Email,
		"role":    user.Role,
		"exp":     now.Add(time.Hour * time.Duration(s.jwtConfig.ExpiryHours)).Unix(),
		// The owner's session generation; ValidateToken refuses the token once
		// the stored counter has moved on (#236).
		tokenVersionClaim: user.TokenVersion,
	}
	if signIn {
		claims[signInTimeClaim] = now.Unix()
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(s.jwtConfig.Secret))
}

// signInTimeClaim is the access-token claim holding the sign-in time in Unix
// seconds (the OpenID Connect name, #532).
const signInTimeClaim = "auth_time"

// SignedInAt returns the auth_time claim of an access token. The signature is
// verified exactly as ValidateToken verifies it; the account is not read. It
// reports false when the token does not verify, or the claim is absent or not
// a number.
func (s *authService) SignedInAt(tokenString string) (time.Time, bool) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(s.jwtConfig.Secret), nil
	})
	if err != nil || !token.Valid {
		return time.Time{}, false
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return time.Time{}, false
	}
	seconds, ok := claims[signInTimeClaim].(float64)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(int64(seconds), 0), true
}

func (s *authService) GenerateTokens(user *models.User) (*AuthTokens, error) {
	accessToken, err := s.GenerateJWT(user)
	if err != nil {
		return nil, err
	}
	return &AuthTokens{AccessToken: accessToken}, nil
}

// sessionTokenSecret is the HMAC key for hashing opaque session tokens:
// the dedicated API key secret when configured, the JWT secret otherwise.
func (s *authService) sessionTokenSecret() string {
	if s.apiKeySecret != "" {
		return s.apiKeySecret
	}
	return s.jwtConfig.Secret
}

// hashOpaqueToken derives the storage form of an opaque token:
// hex(HMAC-SHA256(token)). Only this hash is ever persisted or compared; the
// raw value exists client-side and, for reset tokens, inside the mailed link.
func hashOpaqueToken(token, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(token))
	return hex.EncodeToString(mac.Sum(nil))
}

// generateOpaqueToken mints a 256-bit random token, base64url-encoded.
func generateOpaqueToken() (string, error) {
	buf := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("failed to generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// refreshSession is what a refresh token records about the session it
// belongs to: the family shared by every token of one rotation chain, how
// that session signed in and when.
type refreshSession struct {
	FamilyID     string
	SignInMethod string
	StartedAt    time.Time
}

// refreshFamilyIDBytes is the random length of a new family id, hex-encoded
// to 32 lowercase characters.
const refreshFamilyIDBytes = 16

// newRefreshSession starts a session for a sign-in with the given method
// (models.RefreshTokenSignInPassword or models.RefreshTokenSignInOIDC): a new
// random family id and the current time as its start.
func newRefreshSession(method string) (refreshSession, error) {
	buf := make([]byte, refreshFamilyIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return refreshSession{}, fmt.Errorf("failed to generate refresh token family: %w", err)
	}
	return refreshSession{FamilyID: hex.EncodeToString(buf), SignInMethod: method, StartedAt: time.Now()}, nil
}

// issueRefreshToken mints, hashes and persists a refresh token for the user
// through the given repository — the root one at a login without a transaction
// manager, the transaction-bound one at a wired login, a rotation or a
// password change — returning the raw value for the client. The row records
// the given session: a new one at a sign-in or a password change, the
// presented token's at a rotation. Lifetime comes from
// REFRESH_TOKEN_EXPIRY_DAYS (config JWT.RefreshTokenDays); a single sign-on
// session (oidc) is capped at its start + ssoSessionMaxAge, whichever comes
// first.
func (s *authService) issueRefreshToken(repo repository.RefreshTokenRepository, userID uint, session refreshSession) (string, error) {
	raw, err := generateOpaqueToken()
	if err != nil {
		return "", err
	}

	days := s.jwtConfig.RefreshTokenDays
	if days <= 0 {
		days = 30
	}

	now := time.Now()
	expiresAt := now.Add(time.Duration(days) * 24 * time.Hour)
	if session.SignInMethod == models.RefreshTokenSignInOIDC {
		if limit := session.StartedAt.Add(ssoSessionMaxAge); limit.Before(expiresAt) {
			expiresAt = limit
		}
	}

	token := &models.RefreshToken{
		UserID:       userID,
		TokenHash:    hashOpaqueToken(raw, s.sessionTokenSecret()),
		ExpiresAt:    expiresAt,
		FamilyID:     session.FamilyID,
		SignInMethod: session.SignInMethod,
	}
	// A zero start (a presented row without one) stays NULL; the row gets
	// its own copy of the value either way.
	if !session.StartedAt.IsZero() {
		startedAt := session.StartedAt
		token.SessionStartedAt = &startedAt
	}
	if err := repo.Create(token); err != nil {
		return "", fmt.Errorf("failed to store refresh token: %w", err)
	}
	return raw, nil
}

// accessTokenFor signs the access token of a wired login, a single sign-on
// session or a password change through the instance-local seam when one is
// installed, GenerateJWT otherwise. The token carries auth_time.
func (s *authService) accessTokenFor(user *models.User) (string, error) {
	if s.signAccessToken != nil {
		return s.signAccessToken(user)
	}
	return s.GenerateJWT(user)
}

// refreshedAccessTokenFor signs the access token of a refresh rotation
// through the same seam. It carries no auth_time: a refresh is not a sign-in
// (#532).
func (s *authService) refreshedAccessTokenFor(user *models.User) (string, error) {
	if s.signAccessToken != nil {
		return s.signAccessToken(user)
	}
	return s.signJWT(user, false)
}
