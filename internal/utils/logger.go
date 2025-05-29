package utils

import (
	"fmt"
	"os"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/sirupsen/logrus"
)

var Logger *logrus.Logger

func InitLogger(cfg *config.LoggingConfig) error {
	Logger = logrus.New()

	level, err := logrus.ParseLevel(cfg.Level)
	if err != nil {
		return fmt.Errorf("invalid log level: %w", err)
	}
	Logger.SetLevel(level)

	if cfg.Format == "json" {
		Logger.SetFormatter(&logrus.JSONFormatter{
			TimestampFormat: "2006-01-02T15:04:05.000Z07:00",
		})
	} else {
		Logger.SetFormatter(&logrus.TextFormatter{
			TimestampFormat: "2006-01-02 15:04:05",
			FullTimestamp:   true,
		})
	}

	Logger.SetOutput(os.Stdout)

	return nil
}