package domain

// UploadResponse is returned after a successful image upload.
type UploadResponse struct {
	ID          string `json:"id"`
	ShortID     string `json:"short_id"`
	URL         string `json:"url"`
	DirectURL   string `json:"direct_url"`
	ThumbURL    string `json:"thumb_url"`
	DeleteURL   string `json:"delete_url"`
	DeleteToken string `json:"delete_token"`
	MIMEType    string `json:"mime_type"`
	Extension   string `json:"extension"`
	SizeBytes   int64  `json:"size_bytes"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	SHA256      string `json:"sha256"`
}

// ImageInfoResponse is returned for image detail queries.
type ImageInfoResponse struct {
	ID           string  `json:"id"`
	ShortID      string  `json:"short_id"`
	URL          string  `json:"url"`
	DirectURL    string  `json:"direct_url"`
	ThumbURL     string  `json:"thumb_url"`
	OriginalName string  `json:"original_name"`
	MIMEType     string  `json:"mime_type"`
	SizeBytes    int64   `json:"size_bytes"`
	Width        int     `json:"width"`
	Height       int     `json:"height"`
	SHA256       string  `json:"sha256"`
	ViewCount    int64   `json:"view_count"`
	CreatedAt    string  `json:"created_at"`
	ExpiresAt    *string `json:"expires_at,omitempty"`
}

// ImageListResponse contains a paginated list of images.
type ImageListResponse struct {
	Images     []ImageInfoResponse `json:"images"`
	Total      int64               `json:"total"`
	Page       int                 `json:"page"`
	PerPage    int                 `json:"per_page"`
	TotalPages int                 `json:"total_pages"`
}

// AuthLoginRequest for user login.
type AuthLoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// AuthLoginResponse after successful authentication.
type AuthLoginResponse struct {
	Token     string              `json:"token"`
	ExpiresIn int64               `json:"expires_in"`
	User      *UserPublicResponse `json:"user"`
}

// UserPublicResponse contains only public-safe user fields.
type UserPublicResponse struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	Email        string `json:"email"`
	Role         string `json:"role"`
	QuotaBytes   int64  `json:"quota_bytes"`
	UsedBytes    int64  `json:"used_bytes"`
	QuotaImages  int    `json:"quota_images"`
	UsedImages   int    `json:"used_images"`
	APIKeyPrefix string `json:"api_key_prefix,omitempty"`
	IsActive     bool   `json:"is_active"`
	CreatedAt    string `json:"created_at"`
}

// NewUserPublicResponse creates a safe public response from a User model.
func NewUserPublicResponse(u *User) *UserPublicResponse {
	resp := &UserPublicResponse{
		ID:          u.ID.String(),
		Username:    u.Username,
		Email:       u.Email,
		Role:        u.Role,
		QuotaBytes:  u.QuotaBytes,
		UsedBytes:   u.UsedBytes,
		QuotaImages: u.QuotaImages,
		UsedImages:  u.UsedImages,
		IsActive:    u.IsActive,
		CreatedAt:   u.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
	if u.APIKeyPrefix != nil {
		resp.APIKeyPrefix = *u.APIKeyPrefix
	}
	return resp
}

// CreateUserRequest for user registration (admin).
type CreateUserRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
	Role     string `json:"role,omitempty"`
}

// GenerateAPIKeyResponse after API key creation.
type GenerateAPIKeyResponse struct {
	Key    string `json:"key"`
	Prefix string `json:"prefix"`
}

// AlbumRequest for creating or updating an album.
type AlbumRequest struct {
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	IsPrivate   bool     `json:"is_private,omitempty"`
	ImageIDs    []string `json:"image_ids,omitempty"`
}

// AlbumResponse after album mutation.
type AlbumResponse struct {
	ID          string              `json:"id"`
	ShortID     string              `json:"short_id"`
	Title       string              `json:"title"`
	Description string              `json:"description"`
	IsPrivate   bool                `json:"is_private"`
	URL         string              `json:"url"`
	Images      []ImageInfoResponse `json:"images"`
	CreatedAt   string              `json:"created_at"`
}

// ErrorResponse for API errors.
type ErrorResponse struct {
	Error string `json:"error"`
}

// AuthLoginResponse after successful authentication (with refresh token).
type AuthRefreshResponse struct {
	Token        string              `json:"token"`
	RefreshToken string              `json:"refresh_token"`
	ExpiresIn    int64               `json:"expires_in"`
	User         *UserPublicResponse `json:"user"`
}

// RefreshRequest for token renewal.
type RefreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// ChangePasswordRequest for password changes.
type ChangePasswordRequest struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

// AdminUpdateUserRequest for admin user management.
type AdminUpdateUserRequest struct {
	Role        *string `json:"role,omitempty"`
	IsActive    *bool   `json:"is_active,omitempty"`
	QuotaBytes  *int64  `json:"quota_bytes,omitempty"`
	QuotaImages *int    `json:"quota_images,omitempty"`
}

// AdminStatsResponse for dashboard statistics.
type AdminStatsResponse struct {
	TotalUsers     int    `json:"total_users"`
	ActiveUsers    int    `json:"active_users"`
	TotalImages    int64  `json:"total_images"`
	TotalBytes     int64  `json:"total_bytes"`
	TotalAlbums    int    `json:"total_albums"`
	RecentUploads  int64  `json:"recent_uploads_24h"`
	StorageBackend string `json:"storage_backend"`
}

// UserListResponse for paginated user lists.
type UserListResponse struct {
	Users      []*UserPublicResponse `json:"users"`
	Total      int64                 `json:"total"`
	Page       int                   `json:"page"`
	PerPage    int                   `json:"per_page"`
	TotalPages int                   `json:"total_pages"`
}

// AuditLogResponse for audit log entries.
type AuditLogResponse struct {
	Logs       []AuditLogEntry `json:"logs"`
	Total      int64           `json:"total"`
	Page       int             `json:"page"`
	PerPage    int             `json:"per_page"`
	TotalPages int             `json:"total_pages"`
}

// AuditLogEntry is a single audit log entry for API responses.
type AuditLogEntry struct {
	ID         string `json:"id"`
	ActorName  string `json:"actor_name"`
	Action     string `json:"action"`
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
	Details    string `json:"details"`
	IPAddress  string `json:"ip_address"`
	CreatedAt  string `json:"created_at"`
}
