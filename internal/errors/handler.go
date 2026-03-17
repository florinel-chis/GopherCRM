package errors

import (
	"github.com/gin-gonic/gin"
)

// HandleError handles an error in a Gin handler context
// It extracts the appropriate HTTP status code and response format from typed errors
func HandleError(c *gin.Context, err error) {
	if appErr, ok := AsAppError(err); ok {
		c.JSON(appErr.HTTPStatus, gin.H{
			"success": false,
			"error": gin.H{
				"code":    appErr.Code,
				"message": appErr.Message,
				"details": appErr.Details,
			},
		})
		return
	}

	// Fallback for non-typed errors
	c.JSON(500, gin.H{
		"success": false,
		"error": gin.H{
			"code":    CodeInternal,
			"message": "Internal server error",
		},
	})
}

// HandleErrorWithMessage handles an error with a custom message
func HandleErrorWithMessage(c *gin.Context, err error, message string) {
	if appErr, ok := AsAppError(err); ok {
		c.JSON(appErr.HTTPStatus, gin.H{
			"success": false,
			"error": gin.H{
				"code":    appErr.Code,
				"message": message,
				"details": appErr.Details,
			},
		})
		return
	}

	// Fallback for non-typed errors
	c.JSON(500, gin.H{
		"success": false,
		"error": gin.H{
			"code":    CodeInternal,
			"message": message,
		},
	})
}