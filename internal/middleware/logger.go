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

		c.Next()

		latency := time.Since(start)
		clientIP := c.ClientIP()
		method := c.Request.Method
		statusCode := c.Writer.Status()
		privateErrors := c.Errors.ByType(gin.ErrorTypePrivate)
		bindErrors := c.Errors.ByType(gin.ErrorTypeBind)

		fields := logrus.Fields{
			"request_id": c.GetString("request_id"),
			"client_ip":  clientIP,
			"method":     method,
			"path":       path,
			"status":     statusCode,
			"latency_ms": latency.Milliseconds(),
			"body_size":  c.Writer.Size(),
			"origin":     c.GetHeader("Origin"),
			"user_agent": c.GetHeader("User-Agent"),
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
		if len(bindErrors) > 0 {
			fields["bind_error_count"] = len(bindErrors)
			fields["bind_error"] = utils.LogError(bindErrors.Last().Err)
		}

		logger := utils.Logger.WithFields(fields)

		if len(privateErrors) > 0 {
			logger.WithField("error_count", len(privateErrors)).
				WithError(utils.LogError(privateErrors.Last().Err)).Error("Request failed")
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
