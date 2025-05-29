package middleware

import (
	"fmt"
	"runtime"

	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
)

// Recovery returns a middleware that recovers from panics and returns JSON error response
func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				// Get stack trace
				buf := make([]byte, 1024)
				n := runtime.Stack(buf, false)
				stackTrace := string(buf[:n])
				
				// Log the panic
				logger := utils.GetLogger(c).WithFields(map[string]interface{}{
					"panic":       fmt.Sprintf("%v", err),
					"stack_trace": stackTrace,
					"path":        c.Request.URL.Path,
					"method":      c.Request.Method,
				})
				logger.Error("Panic recovered")
				
				// Abort the request
				c.AbortWithStatus(500)
				
				// Return JSON error response
				utils.RespondInternalError(c)
			}
		}()
		
		c.Next()
	}
}