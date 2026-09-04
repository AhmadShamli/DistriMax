package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/AhmadShamli/DistriMax/internal/auth"
	"github.com/AhmadShamli/DistriMax/internal/db"
)

type RecoverHandler struct {
	db             *db.DB
	recoverySecret string
}

func NewRecoverHandler(database *db.DB, recoverySecret string) *RecoverHandler {
	return &RecoverHandler{
		db:             database,
		recoverySecret: recoverySecret,
	}
}

type recoverRequest struct {
	RecoverySecret string `json:"recovery_secret"`
	Username       string `json:"username"`
	NewPassword    string `json:"new_password"`
}

func (h *RecoverHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		JSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only POST is permitted")
		return
	}

	if h.recoverySecret == "" {
		JSONError(w, http.StatusForbidden, "recovery_disabled", "Break-glass recovery secret is not configured")
		return
	}

	var req recoverRequest
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			JSONError(w, http.StatusBadRequest, "bad_request", "Invalid JSON payload")
			return
		}
	} else {
		_ = r.ParseForm()
		req.RecoverySecret = r.FormValue("recovery_secret")
		req.Username = r.FormValue("username")
		req.NewPassword = r.FormValue("new_password")
	}

	// Constant-time secret comparison
	if subtle.ConstantTimeCompare([]byte(req.RecoverySecret), []byte(h.recoverySecret)) != 1 {
		JSONError(w, http.StatusUnauthorized, "invalid_secret", "Invalid recovery secret")
		return
	}

	if strings.TrimSpace(req.Username) == "" || len(req.NewPassword) < 8 {
		JSONError(w, http.StatusBadRequest, "bad_request", "Username and password (min 8 chars) are required")
		return
	}

	newHash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		JSONError(w, http.StatusInternalServerError, "error", "Failed to hash password")
		return
	}

	// Check if user exists
	user, err := h.db.GetUserByUsername(r.Context(), req.Username)
	if err == nil && user != nil {
		// Update password and re-enable if disabled
		if err := h.db.UpdateUserPassword(r.Context(), user.ID, newHash); err != nil {
			JSONError(w, http.StatusInternalServerError, "error", "Failed to update password")
			return
		}
		_ = h.db.SetUserDisabled(r.Context(), user.ID, false)
		_ = h.db.RevokeUserSessions(r.Context(), user.ID)
	} else if errors.Is(err, db.ErrUserNotFound) {
		// Create new user
		user, err = h.db.CreateUser(r.Context(), req.Username, newHash)
		if err != nil {
			JSONError(w, http.StatusInternalServerError, "error", "Failed to create admin user")
			return
		}
	} else {
		JSONError(w, http.StatusInternalServerError, "database_error", "Failed to query user")
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":  "success",
		"message": "Admin password reset successfully. Please log in with the new credentials.",
	})
}
