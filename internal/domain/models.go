// Package domain contient les modèles métier, les DTOs (Data Transfer Objects)
// et les erreurs sentinelle de PXL.
//
// Les modèles définissent la structure des entités persistées en base de données :
// User, Image, Album. Les DTOs définissent les structures de requête/réponse
// de l'API REST.
package domain

import (
	"time"

	"github.com/google/uuid"
)

// User represents an authenticated user.
type User struct {
	ID           uuid.UUID `json:"id"`
	Username     string    `json:"username"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"`
	AuthVersion  int64     `json:"-"`
	Role         string    `json:"role"`
	IsActive     bool      `json:"is_active"`
	APIKeyHash   *string   `json:"-"`
	APIKeyPrefix *string   `json:"api_key_prefix,omitempty"`
	QuotaBytes   int64     `json:"quota_bytes"`
	UsedBytes    int64     `json:"used_bytes"`
	QuotaImages  int       `json:"quota_images"`
	UsedImages   int       `json:"used_images"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// QuotaRemaining returns bytes of storage quota remaining.
func (u *User) QuotaRemaining() int64 {
	r := u.QuotaBytes - u.UsedBytes
	if r < 0 {
		return 0
	}
	return r
}

// QuotaImagesRemaining returns how many more images the user can upload.
func (u *User) QuotaImagesRemaining() int {
	r := u.QuotaImages - u.UsedImages
	if r < 0 {
		return 0
	}
	return r
}

// Image represents a stored image with metadata.
type Image struct {
	ID             uuid.UUID  `json:"id"`
	ShortID        string     `json:"short_id"`
	UserID         *uuid.UUID `json:"user_id,omitempty"`
	OriginalName   string     `json:"original_name"`
	StoredName     string     `json:"-"`
	StoragePath    string     `json:"-"`
	MIMEType       string     `json:"mime_type"`
	Extension      string     `json:"extension"`
	SizeBytes      int64      `json:"size_bytes"`
	Width          int        `json:"width"`
	Height         int        `json:"height"`
	SHA256         string     `json:"sha256"`
	DeleteToken    string     `json:"-"`
	IsPrivate      bool       `json:"is_private"`
	ViewCount      int64      `json:"view_count"`
	BurnAfterView  bool       `json:"burn_after_view"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	ThumbGenerated bool       `json:"thumb_generated"`
	CreatedAt      time.Time  `json:"created_at"`
	DeletedAt      *time.Time `json:"-"`
}

// Album groups images together.
type Album struct {
	ID          uuid.UUID `json:"id"`
	ShortID     string    `json:"short_id"`
	UserID      uuid.UUID `json:"user_id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	IsPrivate   bool      `json:"is_private"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Images      []Image   `json:"images,omitempty"`
}

// AuditLog enregistre une action traçable (admin, auth, suppression…).
type AuditLog struct {
	ID         uuid.UUID  `json:"id"`
	ActorID    *uuid.UUID `json:"actor_id,omitempty"`
	ActorName  string     `json:"actor_name"`
	Action     string     `json:"action"`
	TargetType string     `json:"target_type"`
	TargetID   string     `json:"target_id"`
	Details    string     `json:"details"` // JSON string
	IPAddress  string     `json:"ip_address"`
	CreatedAt  time.Time  `json:"created_at"`
}

// RefreshToken représente un jeton de renouvellement JWT.
type RefreshToken struct {
	ID          uuid.UUID  `json:"id"`
	UserID      uuid.UUID  `json:"user_id"`
	TokenHash   string     `json:"-"`
	AuthVersion int64      `json:"-"`
	ExpiresAt   time.Time  `json:"expires_at"`
	CreatedAt   time.Time  `json:"created_at"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
}
