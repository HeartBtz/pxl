package domain

import "errors"

// Erreurs sentinelle du domaine PXL, utilisées dans toutes les couches
// (service, handler, repository) pour distinguer les erreurs métier
// des erreurs techniques et renvoyer le bon code HTTP.
var (
	ErrNotFound            = errors.New("not found")
	ErrImageNotFound       = errors.New("image not found")
	ErrImageDeleted        = errors.New("image has been deleted")
	ErrImageExpired        = errors.New("image has expired")
	ErrAlbumNotFound       = errors.New("album not found")
	ErrUserNotFound        = errors.New("user not found")
	ErrUserExists          = errors.New("user already exists")
	ErrInvalidCreds        = errors.New("invalid credentials")
	ErrUserInactive        = errors.New("user is inactive")
	ErrUnauthorized        = errors.New("unauthorized")
	ErrForbidden           = errors.New("forbidden")
	ErrQuotaExceeded       = errors.New("storage quota exceeded")
	ErrQuotaImagesExceeded = errors.New("image count quota exceeded")
	ErrFileTooLarge        = errors.New("file too large")
	ErrInvalidImage        = errors.New("invalid image format")
	ErrBlockedType         = errors.New("image type not allowed")
	ErrInvalidToken        = errors.New("invalid delete token")
	ErrRefreshExpired      = errors.New("refresh token expired")
	ErrRefreshRevoked      = errors.New("refresh token revoked")
	ErrPasswordTooWeak     = errors.New("password must be at least 8 characters")
	ErrInvalidInput        = errors.New("invalid input")
	ErrInsufficientStorage = errors.New("insufficient storage capacity")
)
