package caddyscope

import (
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// checkAuth verifies the basic auth credentials from the request.
// Returns true if the credentials are valid.
func (cs *CaddyScope) checkAuth(r *http.Request) bool {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return false
	}

	if !strings.HasPrefix(auth, "Basic ") {
		return false
	}

	decoded, err := base64.StdEncoding.DecodeString(auth[len("Basic "):])
	if err != nil {
		return false
	}

	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return false
	}

	username, password := parts[0], parts[1]

	// Constant-time comparison for username to prevent timing attacks.
	usernameMatch := subtle.ConstantTimeCompare([]byte(username), []byte(cs.Username)) == 1

	// bcrypt comparison is inherently timing-safe.
	passwordMatch := bcrypt.CompareHashAndPassword([]byte(cs.PasswordHash), []byte(password)) == nil

	return usernameMatch && passwordMatch
}

// requireAuth writes a 401 response with a WWW-Authenticate header.
func requireAuth(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="caddyscope"`)
	http.Error(w, "Unauthorized", http.StatusUnauthorized)
}
