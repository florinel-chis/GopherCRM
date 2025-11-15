package logging

import (
	"context"
	"fmt"
	"os"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// Logger wraps logrus.Logger and provides context-aware logging
type Logger struct {
	logger *logrus.Logger
}

// New creates a new Logger instance
func New(cfg *config.LoggingConfig) (*Logger, error) {
	logger := logrus.New()

	level, err := logrus.ParseLevel(cfg.Level)
	if err != nil {
		return nil, fmt.Errorf("invalid log level: %w", err)
	}
	logger.SetLevel(level)

	if cfg.Format == "json" {
		logger.SetFormatter(&logrus.JSONFormatter{
			TimestampFormat: "2006-01-02T15:04:05.000Z07:00",
		})
	} else {
		logger.SetFormatter(&logrus.TextFormatter{
			TimestampFormat: "2006-01-02 15:04:05",
			FullTimestamp:   true,
		})
	}

	logger.SetOutput(os.Stdout)

	return &Logger{logger: logger}, nil
}

// Logger returns the underlying *logrus.Logger
func (l *Logger) Logger() *logrus.Logger {
	return l.logger
}

// WithContext creates a logger entry with context values
func (l *Logger) WithContext(ctx context.Context) *logrus.Entry {
	entry := l.logger.WithFields(logrus.Fields{})

	// Extract common context values
	if requestID := ctx.Value("request_id"); requestID != nil {
		entry = entry.WithField("request_id", requestID)
	}
	if userID := ctx.Value("user_id"); userID != nil {
		entry = entry.WithField("user_id", userID)
	}
	if userRole := ctx.Value("user_role"); userRole != nil {
		entry = entry.WithField("user_role", userRole)
	}

	return entry
}

// WithGinContext creates a logger entry from a Gin context
func (l *Logger) WithGinContext(c *gin.Context) *logrus.Entry {
	fields := logrus.Fields{}

	if requestID, exists := c.Get("request_id"); exists {
		fields["request_id"] = requestID
	}
	if userID, exists := c.Get("user_id"); exists {
		fields["user_id"] = userID
	}
	if userRole, exists := c.Get("user_role"); exists {
		fields["user_role"] = userRole
	}

	return l.logger.WithFields(fields)
}

// WithField adds a single field to the logger
func (l *Logger) WithField(key string, value interface{}) *logrus.Entry {
	return l.logger.WithField(key, value)
}

// WithFields adds multiple fields to the logger
func (l *Logger) WithFields(fields map[string]interface{}) *logrus.Entry {
	return l.logger.WithFields(fields)
}

// WithError adds an error field to the logger
func (l *Logger) WithError(err error) *logrus.Entry {
	return l.logger.WithError(err)
}

// Debug logs a debug message
func (l *Logger) Debug(args ...interface{}) {
	l.logger.Debug(args...)
}

// Debugf logs a formatted debug message
func (l *Logger) Debugf(format string, args ...interface{}) {
	l.logger.Debugf(format, args...)
}

// Info logs an info message
func (l *Logger) Info(args ...interface{}) {
	l.logger.Info(args...)
}

// Infof logs a formatted info message
func (l *Logger) Infof(format string, args ...interface{}) {
	l.logger.Infof(format, args...)
}

// Warn logs a warning message
func (l *Logger) Warn(args ...interface{}) {
	l.logger.Warn(args...)
}

// Warnf logs a formatted warning message
func (l *Logger) Warnf(format string, args ...interface{}) {
	l.logger.Warnf(format, args...)
}

// Error logs an error message
func (l *Logger) Error(args ...interface{}) {
	l.logger.Error(args...)
}

// Errorf logs a formatted error message
func (l *Logger) Errorf(format string, args ...interface{}) {
	l.logger.Errorf(format, args...)
}

// Fatal logs a fatal message and exits
func (l *Logger) Fatal(args ...interface{}) {
	l.logger.Fatal(args...)
}

// Fatalf logs a formatted fatal message and exits
func (l *Logger) Fatalf(format string, args ...interface{}) {
	l.logger.Fatalf(format, args...)
}

// SetLevel sets the logging level
func (l *Logger) SetLevel(level logrus.Level) {
	l.logger.SetLevel(level)
}
