package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/HeartBtz/pxl/internal/domain"
	"github.com/HeartBtz/pxl/internal/middleware"
	"github.com/google/uuid"
)

// maxJSONBodySize limits JSON request bodies to 1 MB to prevent OOM attacks.
const maxJSONBodySize = 1 << 20 // 1 MB

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		return
	}
}

func safeErrorMessage(err error) string {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return domain.ErrFileTooLarge.Error()
	}
	public := []error{
		domain.ErrInvalidCreds, domain.ErrUserInactive, domain.ErrUserExists,
		domain.ErrUserNotFound, domain.ErrInvalidToken, domain.ErrRefreshExpired,
		domain.ErrRefreshRevoked, domain.ErrPasswordTooWeak, domain.ErrForbidden,
		domain.ErrImageNotFound, domain.ErrQuotaExceeded, domain.ErrQuotaImagesExceeded,
		domain.ErrFileTooLarge, domain.ErrInvalidImage, domain.ErrBlockedType,
		domain.ErrAlbumNotFound, domain.ErrImageDeleted, domain.ErrImageExpired,
		domain.ErrUnauthorized, domain.ErrInvalidInput,
		domain.ErrInsufficientStorage,
	}
	for _, candidate := range public {
		if errors.Is(err, candidate) {
			return candidate.Error()
		}
	}
	return "internal server error"
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, domain.ErrorResponse{Error: msg})
}

func decodeJSON(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, maxJSONBodySize)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return domain.ErrInvalidInput
	}
	return nil
}

// parseUserID extracts and validates the user UUID from context.
// Returns the parsed UUID and true on success. On failure, writes a 401/500 error.
func parseUserID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	uidStr, ok := middleware.GetUserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return uuid.Nil, false
	}
	uid, err := uuid.Parse(uidStr)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "invalid user context")
		return uuid.Nil, false
	}
	return uid, true
}
