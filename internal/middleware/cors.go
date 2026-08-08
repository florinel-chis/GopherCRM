package middleware

import (
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
