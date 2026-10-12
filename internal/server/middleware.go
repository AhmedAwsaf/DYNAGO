package server

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// corsMiddleware adds CORS headers for browser clients.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// loggingMiddleware logs each request.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		log.Printf("[http] %s %s %d %s", r.Method, r.URL.Path, sw.status, time.Since(start).Round(time.Microsecond))
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(code int) {
	sw.status = code
	sw.ResponseWriter.WriteHeader(code)
}

func (sw *statusWriter) Flush() {
	if f, ok := sw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

type contextKey string

const userContextKey contextKey = "dynago_user"

// AuthUser represents an authenticated principal.
type AuthUser struct {
	Username string `json:"username"`
	Role     string `json:"role"`
}

// ContextWithUser associates an authenticated user with a request context.
func ContextWithUser(ctx context.Context, user *AuthUser) context.Context {
	return context.WithValue(ctx, userContextKey, user)
}

// UserFromContext retrieves the authenticated user from a request context.
func UserFromContext(ctx context.Context) (*AuthUser, bool) {
	u, ok := ctx.Value(userContextKey).(*AuthUser)
	return u, ok
}

// jwtMiddleware validates Bearer tokens on non-public paths.
// Supports Authorization header ("Bearer <token>") and query parameter ("?token=<token>" for SSE/WebSocket).
func jwtMiddleware(next http.Handler, secret string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip auth for OPTIONS (CORS preflight)
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		var tokenStr string
		auth := r.Header.Get("Authorization")
		if strings.HasPrefix(auth, "Bearer ") {
			tokenStr = strings.TrimPrefix(auth, "Bearer ")
		} else if qToken := r.URL.Query().Get("token"); qToken != "" {
			tokenStr = qToken
		}

		if tokenStr == "" {
			// Public endpoints that do not require authentication
			if r.URL.Path == "/healthz" ||
				r.URL.Path == "/" ||
				r.URL.Path == "/ui" ||
				r.URL.Path == "/api/v1/_ui/meta" ||
				r.URL.Path == "/api/v1/_auth/login" ||
				r.URL.Path == "/api/v1/_auth/register" ||
				r.URL.Path == "/api/v1/functions" {
				next.ServeHTTP(w, r)
				return
			}

			// Functions & RPC endpoints evaluate public vs private permission inside handleInvokeFunction
			if strings.HasPrefix(r.URL.Path, "/api/v1/functions/") || strings.HasPrefix(r.URL.Path, "/api/v1/rpc/") {
				next.ServeHTTP(w, r)
				return
			}

			// Table CRUD endpoints evaluate public vs private permission inside checkTablePermission
			if strings.HasPrefix(r.URL.Path, "/api/v1/") && !strings.HasPrefix(r.URL.Path, "/api/v1/_") {
				next.ServeHTTP(w, r)
				return
			}

			// Schema discovery and endpoint listing
			if r.URL.Path == "/api/v1/_schema" || strings.HasPrefix(r.URL.Path, "/api/v1/_schema/") || r.URL.Path == "/api/v1/_endpoints" {
				next.ServeHTTP(w, r)
				return
			}

			// UI data explorer endpoint for table (/api/v1/_ui/data/{table}) evaluates checkTablePermission inside handler
			if strings.HasPrefix(r.URL.Path, "/api/v1/_ui/data/") {
				next.ServeHTTP(w, r)
				return
			}

			jsonError(w, http.StatusUnauthorized, "missing or invalid Authorization header or token query parameter")
			return
		}

		token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, jwt.ErrSignatureInvalid
			}
			return []byte(secret), nil
		})
		if err != nil || !token.Valid {
			jsonError(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}

		if claims, ok := token.Claims.(jwt.MapClaims); ok {
			username, _ := claims["sub"].(string)
			role, _ := claims["role"].(string)
			r = r.WithContext(ContextWithUser(r.Context(), &AuthUser{Username: username, Role: role}))
		}

		next.ServeHTTP(w, r)
	})
}
