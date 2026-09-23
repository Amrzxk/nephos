package apiserver

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"

	"github.com/Amrzxk/nephos/internal/apiserver/generated"
)

func requireBearer(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/health" {
			next.ServeHTTP(w, r)
			return
		}
		given, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" || subtle.ConstantTimeCompare([]byte(given), []byte(token)) != 1 {
			writeJSON(w, http.StatusUnauthorized, generated.Error{
				Code: "AuthFailure", Message: "missing or invalid bearer token",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if parsed, _, err := net.SplitHostPort(host); err == nil {
			host = parsed
		}
		if (host != "localhost" && host != "127.0.0.1" && host != "::1") || r.Header.Get("Origin") != "" {
			writeJSON(w, http.StatusForbidden, generated.Error{
				Code: "AccessDenied", Message: "non-local host or browser origin rejected",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}
