package middleware

import (
	"time"

	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
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

		if raw != "" {
			path = path + "?" + raw
		}

		logger := utils.Logger.WithFields(map[string]interface{}{
			"request_id": c.GetString("request_id"),
			"client_ip":  clientIP,
			"method":     method,
			"path":       path,
			"status":     statusCode,
			"latency_ms": latency.Milliseconds(),
			"user_agent": c.Request.UserAgent(),
		})

		if errorMessage != "" {
			logger.WithField("error", errorMessage).Error("Request failed")
		} else if statusCode >= 500 {
			logger.Error("Server error")
		} else if statusCode >= 400 {
			logger.Warn("Client error")
		} else {
			logger.Info("Request completed")
		}
	}
}