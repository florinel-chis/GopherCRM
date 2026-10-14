package utils

import (
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// GetLogger returns a logger with context fields from the gin context
func GetLogger(c *gin.Context) *logrus.Entry {
	fields := logrus.Fields{
		"request_id": c.GetString("request_id"),
	}

	// Add user context if available
	if userID, exists := c.Get("user_id"); exists {
		fields["user_id"] = userID
	}

	if userRole, exists := c.Get("user_role"); exists {
		fields["user_role"] = userRole
	}

	return Logger.WithFields(fields)
}

// LogServiceCall logs service method calls without arbitrary argument values.
// Attach safe identifiers or counts explicitly to logger instead.
func LogServiceCall(logger *logrus.Entry, service, method string, _ ...interface{}) *logrus.Entry {
	entry := logger.WithFields(logrus.Fields{
		"service": service,
		"method":  method,
	})

	entry.Debug("Service call started")
	return entry
}

// LogServiceResponse logs completion without response payloads.
func LogServiceResponse(logger *logrus.Entry, err error, _ ...interface{}) {
	if err != nil {
		logger.WithError(LogError(err)).Error("Service call failed")
	} else {
		logger.Debug("Service call completed")
	}
}

// LogHandlerStart logs the start of a handler
func LogHandlerStart(c *gin.Context, handler string) *logrus.Entry {
	logger := GetLogger(c).WithField("handler", handler)
	logger.Debug("Handler started")
	return logger
}

// LogHandlerResponse logs the HTTP status without serializing response payloads.
func LogHandlerResponse(logger *logrus.Entry, statusCode int, _ interface{}) {
	logger.WithFields(logrus.Fields{
		"status_code": statusCode,
	}).Debug("Handler completed")
}
