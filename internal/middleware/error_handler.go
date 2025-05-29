package middleware

import (
	"net/http"

	"github.com/florinel-chis/gocrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
)

func ErrorHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()

		// Don't process if response was already written
		if c.Writer.Written() {
			return
		}

		if len(c.Errors) > 0 {
			err := c.Errors.Last()
			
			logger := utils.GetLogger(c).WithField("error", err.Error())

			switch err.Type {
			case gin.ErrorTypePublic:
				logger.Warn("Public error")
				// Status should already be set
				if c.Writer.Status() == http.StatusOK {
					c.Status(http.StatusBadRequest)
				}
			case gin.ErrorTypeBind:
				logger.Warn("Binding error")
				
				// Parse validation errors
				if ve, ok := err.Err.(validator.ValidationErrors); ok {
					errors := make(map[string]string)
					for _, fe := range ve {
						field := fe.Field()
						tag := fe.Tag()
						
						// Generate user-friendly error messages
						switch tag {
						case "required":
							errors[field] = field + " is required"
						case "email":
							errors[field] = field + " must be a valid email address"
						case "min":
							errors[field] = field + " must be at least " + fe.Param() + " characters long"
						case "max":
							errors[field] = field + " must be at most " + fe.Param() + " characters long"
						case "gte":
							errors[field] = field + " must be greater than or equal to " + fe.Param()
						case "lte":
							errors[field] = field + " must be less than or equal to " + fe.Param()
						case "uuid":
							errors[field] = field + " must be a valid UUID"
						case "oneof":
							errors[field] = field + " must be one of: " + fe.Param()
						default:
							errors[field] = field + " is invalid"
						}
					}
					
					utils.RespondValidationError(c, errors)
				} else {
					// Generic binding error
					utils.RespondBadRequest(c, "Invalid request format")
				}
			default:
				logger.Error("Internal error")
				// Don't expose internal errors to clients
				utils.RespondInternalError(c)
			}
		}
	}
}