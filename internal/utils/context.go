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

// LogServiceCall logs service method calls
func LogServiceCall(logger *logrus.Entry, service, method string, args ...interface{}) *logrus.Entry {
	entry := logger.WithFields(logrus.Fields{
		"service": service,
		"method":  method,
	})

	if len(args) > 0 {
		entry = entry.WithField("args", args)
	}

	entry.Debug("Service call started")
	return entry
}

// LogServiceResponse logs service method responses
func LogServiceResponse(logger *logrus.Entry, err error, result ...interface{}) {
	if err != nil {
		logger.WithError(err).Error("Service call failed")
	} else {
		fields := logrus.Fields{}
		if len(result) > 0 {
			fields["result"] = result[0]
		}
		logger.WithFields(fields).Debug("Service call completed")
	}
}

// LogRepositoryOperation logs repository operations
func LogRepositoryOperation(logger *logrus.Entry, repo, operation string, query ...interface{}) *logrus.Entry {
	entry := logger.WithFields(logrus.Fields{
		"repository": repo,
		"operation":  operation,
	})

	if len(query) > 0 {
		entry = entry.WithField("query", query)
	}

	entry.Debug("Repository operation started")
	return entry
}

// LogHandlerStart logs the start of a handler
func LogHandlerStart(c *gin.Context, handler string) *logrus.Entry {
	logger := GetLogger(c).WithField("handler", handler)
	logger.Debug("Handler started")
	return logger
}

// LogHandlerResponse logs handler responses
func LogHandlerResponse(logger *logrus.Entry, statusCode int, response interface{}) {
	logger.WithFields(logrus.Fields{
		"status_code": statusCode,
		"response":    response,
	}).Debug("Handler completed")
}