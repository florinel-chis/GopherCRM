package middleware

import (
	"strings"
	"time"

	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

func Logger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		raw := c.Request.URL.RawQuery

		c.Next()

		latency := time.Since(start)
		clientIP := c.ClientIP()
		method := c.Request.Method
		statusCode := c.Writer.Status()
		errorMessage := c.Errors.ByType(gin.ErrorTypePrivate).String()
		bindErrors := c.Errors.ByType(gin.ErrorTypeBind).String()

		if raw != "" {
			path = path + "?" + raw
		}

		fields := logrus.Fields{
			"request_id":   c.GetString("request_id"),
			"client_ip":    clientIP,
			"method":       method,
			"path":         path,
			"status":       statusCode,
			"latency_ms":   latency.Milliseconds(),
			"body_size":    c.Writer.Size(),
			"origin":       c.GetHeader("Origin"),
		}

		// Add user context if authenticated
		if userID, exists := c.Get("user_id"); exists {
			fields["user_id"] = userID
		}
		if userRole, exists := c.Get("user_role"); exists {
			fields["user_role"] = userRole
		}

		// Add auth method used
		authHeader := c.GetHeader("Authorization")
		if strings.HasPrefix(authHeader, "Bearer ") {
			fields["auth_method"] = "bearer"
		} else if strings.HasPrefix(authHeader, "ApiKey ") {
			fields["auth_method"] = "apikey"
		}

		// Add rate limit headers if present
		if rl := c.Writer.Header().Get("X-RateLimit-Remaining"); rl != "" {
			fields["rate_limit_remaining"] = rl
		}

		// Add binding/validation errors for debugging
		if bindErrors != "" {
			fields["bind_error"] = bindErrors
		}

		logger := utils.Logger.WithFields(fields)

		if errorMessage != "" {
			logger.WithField("error", errorMessage).Error("Request failed")
		} else if statusCode >= 500 {
			logger.Error("Server error")
		} else if statusCode == 429 {
			logger.Warn("Rate limited")
		} else if statusCode >= 400 {
			logger.Warn("Client error")
		} else {
			logger.Info("Request completed")
		}
	}
}