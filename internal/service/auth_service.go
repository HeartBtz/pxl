package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/HeartBtz/pxl/internal/config"
	"github.com/HeartBtz/pxl/internal/crypto"
	"github.com/HeartBtz/pxl/internal/domain"
	"github.com/HeartBtz/pxl/internal/repository"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// AuthService gère l'authentification et la gestion des utilisateurs PXL.
// Fournit : login (JWT + refresh token), validation de tokens, vérification
// de clés API, hachage bcrypt, refresh tokens, changement de mot de passe,
// et gestion admin (liste, rôle, suspension, quotas).
type AuthService struct {
	userRepo    *repository.UserRepository
	refreshRepo *repository.RefreshTokenRepository
	cfg         *config.Config
	log         *slog.Logger
}

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,30}$`)

func NewAuthService(ur *repository.UserRepository, rr *repository.RefreshTokenRepository, cfg *config.Config, logger *slog.Logger) *AuthService {
	return &AuthService{userRepo: ur, refreshRepo: rr, cfg: cfg, log: logger}
}

func (s *AuthService) BrowserCookieSecure() bool {
	return strings.HasPrefix(strings.ToLower(s.cfg.Server.BaseURL), "https://")
}

func (s *AuthService) RefreshExpiry() time.Duration { return s.cfg.Auth.RefreshExpiry }

// Claims represents JWT payload.
type Claims struct {
	jwt.RegisteredClaims
	UserID      string `json:"uid"`
	Username    string `json:"usr"`
	Role        string `json:"role"`
	AuthTag     string `json:"atg"`
	AuthVersion int64  `json:"av,omitempty"`
}

// Login authenticates with username/password, returns JWT + refresh token.
func (s *AuthService) Login(ctx context.Context, req *domain.AuthLoginRequest) (*domain.AuthRefreshResponse, error) {
	username := strings.TrimSpace(req.Username)
	if username == "" || req.Password == "" || len(req.Password) > 72 {
		return nil, domain.ErrInvalidCreds
	}
	user, err := s.userRepo.GetByUsername(ctx, username)
	if err != nil {
		return nil, domain.ErrInvalidCreds
	}
	if !user.IsActive {
		return nil, domain.ErrUserInactive
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		return nil, domain.ErrInvalidCreds
	}

	token, exp, err := s.generateJWT(user)
	if err != nil {
		return nil, fmt.Errorf("generate jwt: %w", err)
	}

	refreshToken, err := s.createRefreshToken(ctx, user)
	if err != nil {
		return nil, fmt.Errorf("create refresh token: %w", err)
	}

	return &domain.AuthRefreshResponse{
		Token:        token,
		RefreshToken: refreshToken,
		ExpiresIn:    int64(exp.Seconds()),
		User:         domain.NewUserPublicResponse(user),
	}, nil
}

// LoginCompat retourne un AuthLoginResponse (sans refresh token) pour compatibilité.
func (s *AuthService) LoginCompat(ctx context.Context, req *domain.AuthLoginRequest) (*domain.AuthLoginResponse, error) {
	resp, err := s.Login(ctx, req)
	if err != nil {
		return nil, err
	}
	return &domain.AuthLoginResponse{
		Token:     resp.Token,
		ExpiresIn: resp.ExpiresIn,
		User:      resp.User,
	}, nil
}

// RefreshJWT renouvelle un JWT à partir d'un refresh token valide.
func (s *AuthService) RefreshJWT(ctx context.Context, refreshTokenStr string) (*domain.AuthRefreshResponse, error) {
	hash := crypto.HashAPIKey(refreshTokenStr)
	newRefresh := crypto.GenerateToken(32)
	user, err := s.refreshRepo.Rotate(ctx, hash, crypto.HashAPIKey(newRefresh), time.Now().Add(s.cfg.Auth.RefreshExpiry))
	if err != nil {
		return nil, err
	}

	token, exp, err := s.generateJWT(user)
	if err != nil {
		return nil, fmt.Errorf("generate jwt: %w", err)
	}

	return &domain.AuthRefreshResponse{
		Token:        token,
		RefreshToken: newRefresh,
		ExpiresIn:    int64(exp.Seconds()),
		User:         domain.NewUserPublicResponse(user),
	}, nil
}

// Logout revokes all access and refresh sessions for the user.
func (s *AuthService) Logout(ctx context.Context, userID uuid.UUID) error {
	return s.userRepo.RevokeSessions(ctx, userID)
}

// Register creates a new user. Password is hashed with bcrypt.
func (s *AuthService) Register(ctx context.Context, req *domain.CreateUserRequest) (*domain.User, error) {
	username := strings.TrimSpace(req.Username)
	email := strings.ToLower(strings.TrimSpace(req.Email))
	parsedEmail, emailErr := mail.ParseAddress(email)
	if !usernamePattern.MatchString(username) || emailErr != nil || parsedEmail.Address != email || len(email) > 254 || len(req.Password) < 8 || len(req.Password) > 72 {
		return nil, domain.ErrInvalidInput
	}
	req.Username = username
	req.Email = email
	exists, err := s.userRepo.Exists(ctx, req.Username, req.Email)
	if err != nil {
		return nil, fmt.Errorf("check exists: %w", err)
	}
	if exists {
		return nil, domain.ErrUserExists
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("bcrypt: %w", err)
	}

	role := "user"
	if req.Role == "admin" {
		role = "admin"
	}

	user := &domain.User{
		Username:     req.Username,
		Email:        req.Email,
		PasswordHash: string(hash),
		Role:         role,
		IsActive:     true,
		QuotaBytes:   s.cfg.Auth.DefaultQuotaBytes,
		QuotaImages:  s.cfg.Auth.DefaultQuotaImages,
	}
	if err := s.userRepo.Create(ctx, user); err != nil {
		return nil, err
	}
	return user, nil
}

// ChangePassword changes a user's password after verifying the old one.
func (s *AuthService) ChangePassword(ctx context.Context, userID uuid.UUID, oldPass, newPass string) error {
	if len(newPass) < 8 {
		return domain.ErrPasswordTooWeak
	}
	if len(newPass) > 72 || len(oldPass) > 72 {
		return domain.ErrInvalidInput
	}
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(oldPass)); err != nil {
		return domain.ErrInvalidCreds
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPass), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("bcrypt: %w", err)
	}
	if err := s.userRepo.UpdatePassword(ctx, userID, user.PasswordHash, string(hash)); err != nil {
		return err
	}
	return nil
}

// ValidateJWT parses and validates a JWT token string.
func (s *AuthService) ValidateJWT(tokenStr string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
		return []byte(s.cfg.Auth.JWTSecret), nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithIssuer("pxl"), jwt.WithExpirationRequired())
	if err != nil || !token.Valid {
		return nil, domain.ErrInvalidToken
	}
	return claims, nil
}

// ValidateJWTUser resolves current account state so suspension and role changes
// take effect immediately instead of waiting for JWT expiry.
func (s *AuthService) ValidateJWTUser(ctx context.Context, tokenStr string) (*Claims, *domain.User, error) {
	claims, err := s.ValidateJWT(tokenStr)
	if err != nil {
		return nil, nil, err
	}
	userID, err := uuid.Parse(claims.UserID)
	if err != nil || claims.Subject != userID.String() {
		return nil, nil, domain.ErrInvalidToken
	}
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil || !user.IsActive {
		return nil, nil, domain.ErrUserInactive
	}
	if claims.IssuedAt == nil || claims.AuthVersion != user.AuthVersion || !hmac.Equal([]byte(claims.AuthTag), []byte(s.authTag(user.PasswordHash))) {
		return nil, nil, domain.ErrInvalidToken
	}
	return claims, user, nil
}

// ValidateAPIKey checks an API key against stored hashes.
func (s *AuthService) ValidateAPIKey(ctx context.Context, key string) (*domain.User, error) {
	hash := crypto.HashAPIKey(key)
	user, err := s.userRepo.GetByAPIKeyHash(ctx, hash)
	if err != nil {
		return nil, domain.ErrInvalidToken
	}
	if !user.IsActive {
		return nil, domain.ErrUserInactive
	}
	return user, nil
}

// GenerateAPIKey creates a new API key for a user.
func (s *AuthService) GenerateAPIKey(ctx context.Context, userID uuid.UUID) (string, error) {
	key := "pxl_" + crypto.GenerateToken(32)
	hash := crypto.HashAPIKey(key)
	prefix := key[:11]

	if err := s.userRepo.SetAPIKey(ctx, userID, hash, prefix); err != nil {
		return "", fmt.Errorf("set api key: %w", err)
	}
	return key, nil
}

// GetUser returns a user by ID.
func (s *AuthService) GetUser(ctx context.Context, userID uuid.UUID) (*domain.User, error) {
	return s.userRepo.GetByID(ctx, userID)
}

// ListUsers returns paginated users (admin-only) with optional search.
func (s *AuthService) ListUsers(ctx context.Context, page, perPage int, search string) ([]*domain.User, int64, error) {
	return s.userRepo.ListAll(ctx, page, perPage, search)
}

// UpdateUserRole changes a user's role (admin-only).
func (s *AuthService) UpdateUserRole(ctx context.Context, userID uuid.UUID, role string) error {
	if role != "admin" && role != "user" {
		return domain.ErrInvalidInput
	}
	return s.userRepo.AdminUpdate(ctx, userID, &domain.AdminUpdateUserRequest{Role: &role})
}

// SetUserActive enables or disables a user (admin-only).
func (s *AuthService) SetUserActive(ctx context.Context, userID uuid.UUID, active bool) error {
	return s.userRepo.AdminUpdate(ctx, userID, &domain.AdminUpdateUserRequest{IsActive: &active})
}

// UpdateUserQuotas updates user quotas (admin-only).
func (s *AuthService) UpdateUserQuotas(ctx context.Context, userID uuid.UUID, quotaBytes *int64, quotaImages *int) error {
	if quotaBytes != nil && *quotaBytes < 0 {
		return domain.ErrInvalidInput
	}
	if quotaImages != nil && *quotaImages < 0 {
		return domain.ErrInvalidInput
	}
	return s.userRepo.UpdateQuotas(ctx, userID, quotaBytes, quotaImages)
}

// AdminUpdateUser applies admin update request.
func (s *AuthService) AdminUpdateUser(ctx context.Context, actorID, userID uuid.UUID, req *domain.AdminUpdateUserRequest) error {
	if req.Role == nil && req.IsActive == nil && req.QuotaBytes == nil && req.QuotaImages == nil {
		return domain.ErrInvalidInput
	}
	if req.Role != nil && *req.Role != "admin" && *req.Role != "user" {
		return domain.ErrInvalidInput
	}
	if req.QuotaBytes != nil && *req.QuotaBytes < 0 {
		return domain.ErrInvalidInput
	}
	if req.QuotaImages != nil && *req.QuotaImages < 0 {
		return domain.ErrInvalidInput
	}
	current, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return err
	}
	nextRole, nextActive := current.Role, current.IsActive
	if req.Role != nil {
		nextRole = *req.Role
	}
	if req.IsActive != nil {
		nextActive = *req.IsActive
	}
	if actorID == userID && (nextRole != "admin" || !nextActive) {
		return domain.ErrForbidden
	}
	if current.Role == "admin" && current.IsActive && (nextRole != "admin" || !nextActive) {
		count, err := s.userRepo.CountActiveAdmins(ctx)
		if err != nil {
			return err
		}
		if count <= 1 {
			return domain.ErrForbidden
		}
	}
	return s.userRepo.AdminUpdate(ctx, userID, req)
}

// DeleteUser supprime un utilisateur (admin).
func (s *AuthService) DeleteUser(ctx context.Context, actorID, userID uuid.UUID) error {
	if actorID == userID {
		return domain.ErrForbidden
	}
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return err
	}
	if user.Role == "admin" && user.IsActive {
		count, err := s.userRepo.CountActiveAdmins(ctx)
		if err != nil {
			return err
		}
		if count <= 1 {
			return domain.ErrForbidden
		}
	}
	_ = s.refreshRepo.RevokeAllForUser(ctx, userID)
	return s.userRepo.Delete(ctx, userID)
}

// EnsureAdminExists creates admin user on first run if not exists.
func (s *AuthService) EnsureAdminExists(ctx context.Context) error {
	if s.cfg.Auth.AdminUsername == "" {
		return nil
	}
	_, err := s.userRepo.GetByUsername(ctx, s.cfg.Auth.AdminUsername)
	if err == nil {
		return nil // admin already exists
	}
	if !errors.Is(err, domain.ErrUserNotFound) {
		return err
	}
	if s.cfg.Auth.AdminPassword == "" {
		return fmt.Errorf("admin user %q does not exist; PXL_AUTH_ADMIN_PASSWORD is required for initial bootstrap", s.cfg.Auth.AdminUsername)
	}
	s.log.Info("creating admin user", slog.String("username", s.cfg.Auth.AdminUsername))
	_, err = s.Register(ctx, &domain.CreateUserRequest{
		Username: s.cfg.Auth.AdminUsername,
		Email:    s.cfg.Auth.AdminUsername + "@localhost",
		Password: s.cfg.Auth.AdminPassword,
		Role:     "admin",
	})
	return err
}

func (s *AuthService) generateJWT(user *domain.User) (string, time.Duration, error) {
	exp := s.cfg.Auth.JWTExpiry
	claims := &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(exp)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "pxl",
			Subject:   user.ID.String(),
		},
		UserID:      user.ID.String(),
		Username:    user.Username,
		Role:        user.Role,
		AuthTag:     s.authTag(user.PasswordHash),
		AuthVersion: user.AuthVersion,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(s.cfg.Auth.JWTSecret))
	return signed, exp, err
}

// authTag retains the password binding of existing access tokens alongside the
// revocation version. Unrelated profile/quota updates do not log users out.
func (s *AuthService) authTag(passwordHash string) string {
	mac := hmac.New(sha256.New, []byte(s.cfg.Auth.JWTSecret))
	_, _ = mac.Write([]byte("pxl-auth-tag\x00"))
	_, _ = mac.Write([]byte(passwordHash))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *AuthService) createRefreshToken(ctx context.Context, user *domain.User) (string, error) {
	rawToken := crypto.GenerateToken(32)
	hash := crypto.HashAPIKey(rawToken)

	rt := &domain.RefreshToken{
		UserID:      user.ID,
		AuthVersion: user.AuthVersion,
		TokenHash:   hash,
		ExpiresAt:   time.Now().Add(s.cfg.Auth.RefreshExpiry),
	}
	if err := s.refreshRepo.Create(ctx, rt); err != nil {
		return "", fmt.Errorf("create refresh token: %w", err)
	}
	return rawToken, nil
}
