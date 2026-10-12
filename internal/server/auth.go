package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token     string    `json:"token"`
	TokenType string    `json:"token_type"`
	ExpiresIn int       `json:"expires_in"`
	ExpiresAt string    `json:"expires_at"`
	User      *AuthUser `json:"user"`
}

// handleAuthLogin authenticates user credentials and returns a signed JWT.
func (h *handler) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if h.cfg.JWTSecret == "" {
		jsonError(w, http.StatusBadRequest, "authentication is disabled (DYNAGO_JWT_SECRET is not configured)")
		return
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		jsonError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	bodyBytes = bytes.Trim(bytes.TrimSpace(bodyBytes), "'")

	var req loginRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request json: "+err.Error())
		return
	}

	authUser, ok := h.authenticateUser(r.Context(), req.Username, req.Password)
	if !ok {
		jsonError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	expiresAt := time.Now().Add(h.cfg.AuthTokenTTL)
	claims := jwt.MapClaims{
		"sub":  authUser.Username,
		"role": authUser.Role,
		"iat":  time.Now().Unix(),
		"exp":  expiresAt.Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString([]byte(h.cfg.JWTSecret))
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "failed to sign token: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(loginResponse{
		Token:     tokenString,
		TokenType: "Bearer",
		ExpiresIn: int(h.cfg.AuthTokenTTL.Seconds()),
		ExpiresAt: expiresAt.Format(time.RFC3339),
		User:      authUser,
	})
}

// handleAuthMe returns the current authenticated identity.
func (h *handler) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"authenticated": false,
			"auth_enabled":  h.cfg.JWTSecret != "",
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"authenticated": true,
		"auth_enabled":  true,
		"user":          user,
	})
}

// handleAuthRegister creates a new user account (role: viewer) and returns a signed JWT.
func (h *handler) handleAuthRegister(w http.ResponseWriter, r *http.Request) {
	if h.cfg.JWTSecret == "" {
		jsonError(w, http.StatusBadRequest, "authentication is disabled (DYNAGO_JWT_SECRET is not configured)")
		return
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		jsonError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	bodyBytes = bytes.Trim(bytes.TrimSpace(bodyBytes), "'")

	var req loginRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request json: "+err.Error())
		return
	}

	req.Username = strings.ToLower(strings.TrimSpace(req.Username))
	if req.Username == "" || req.Password == "" {
		jsonError(w, http.StatusBadRequest, "username and password are required")
		return
	}

	hash, err := hashPassword(req.Password)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "failed to hash password: "+err.Error())
		return
	}

	var exists bool
	_ = h.pool.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM _dynago_users WHERE LOWER(username) = LOWER($1))", req.Username).Scan(&exists)
	if exists {
		jsonError(w, http.StatusConflict, "username already exists")
		return
	}

	_, err = h.pool.Exec(r.Context(),
		"INSERT INTO _dynago_users (username, password_hash, role) VALUES ($1, $2, 'viewer')",
		req.Username, hash,
	)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "failed to create user: "+err.Error())
		return
	}

	authUser := &AuthUser{Username: req.Username, Role: "viewer"}
	expiresAt := time.Now().Add(h.cfg.AuthTokenTTL)
	claims := jwt.MapClaims{
		"sub":  authUser.Username,
		"role": authUser.Role,
		"iat":  time.Now().Unix(),
		"exp":  expiresAt.Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString([]byte(h.cfg.JWTSecret))
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "failed to sign token: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(loginResponse{
		Token:     tokenString,
		TokenType: "Bearer",
		ExpiresIn: int(h.cfg.AuthTokenTTL.Seconds()),
		ExpiresAt: expiresAt.Format(time.RFC3339),
		User:      authUser,
	})
}

