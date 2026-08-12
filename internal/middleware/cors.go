package middleware

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

// defaultAllowedOrigins covers local development: the Vite dev server, the
// dockerised UI on its default port, and the API's own origin.
var defaultAllowedOrigins = []string{
	"http://localhost:5173",
	"http://127.0.0.1:5173", // Also allow 127.0.0.1
	"http://localhost:3000",
	"http://127.0.0.1:3000",
	"http://localhost:8080",
}

// CORS builds the CORS middleware. extraOrigins (from CORS_ALLOWED_ORIGINS)
// are allowed in addition to the development defaults — a deployment that
// serves the UI anywhere else must list its origin there, because gin-contrib
// rejects any request whose Origin header is not allowlisted with a bare 403
// before the request reaches the logger or the handlers.
func CORS(extraOrigins ...string) gin.HandlerFunc {
	return CORSWithPublicPrefixes(nil, extraOrigins...)
}

// CORSWithPublicPrefixes builds the CORS middleware with a carve-out for
// deliberately public endpoints. Requests whose path starts with one of
// publicPrefixes are served a permissive, credential-less policy: the request's
// own Origin is echoed back, no cookies or Authorization are ever allowed, and
// preflights are answered directly. Those endpoints are meant to be called from
// arbitrary third-party sites, so the allowlist below — and its bare 403 for
// unknown origins — must not apply to them. Any per-caller restriction on such
// routes belongs in the handler, where it can produce a real error response.
//
// Every other path keeps the strict allowlist behaviour described on CORS.
func CORSWithPublicPrefixes(publicPrefixes []string, extraOrigins ...string) gin.HandlerFunc {
	strict := strictCORS(extraOrigins...)
	prefixes := make([]string, 0, len(publicPrefixes))
	for _, prefix := range publicPrefixes {
		if prefix != "" {
			prefixes = append(prefixes, prefix)
		}
	}
	if len(prefixes) == 0 {
		return strict
	}

	return func(c *gin.Context) {
		if !hasAnyPrefix(c.Request.URL.Path, prefixes) {
			strict(c)
			return
		}

		origin := c.GetHeader("Origin")
		if origin == "" {
			// Not a cross-origin browser call (curl, server-to-server,
			// same-origin fetch): nothing to negotiate.
			c.Next()
			return
		}

		header := c.Writer.Header()
		header.Set("Access-Control-Allow-Origin", origin)
		header.Add("Vary", "Origin")
		header.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		header.Set("Access-Control-Allow-Headers", "Content-Type")

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

func hasAnyPrefix(path string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func strictCORS(extraOrigins ...string) gin.HandlerFunc {
	origins := make([]string, 0, len(defaultAllowedOrigins)+len(extraOrigins))
	seen := make(map[string]bool, cap(origins))
	for _, origin := range append(append([]string{}, defaultAllowedOrigins...), extraOrigins...) {
		if origin != "" && !seen[origin] {
			seen[origin] = true
			origins = append(origins, origin)
		}
	}
	return cors.New(cors.Config{
		AllowOrigins:     origins,
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization", "X-Request-ID"},
		ExposeHeaders:    []string{"Content-Length", "X-Request-ID"},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	})
}
