package middleware

import (
	"net/http"
	"reflect"

	"github.com/florinel-chis/gophercrm/internal/utils"
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
						errors[field] = validationMessage(fe)
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

// validationMessage turns one validator failure into the message returned
// for its field. min and max bound the value of a number and the length of
// anything else, so a probability of 101 is reported as "at most 100", not
// "at most 100 characters long".
func validationMessage(fe validator.FieldError) string {
	field := fe.Field()
	switch fe.Tag() {
	case "required":
		return field + " is required"
	case "email":
		return field + " must be a valid email address"
	case "min":
		if isNumericKind(fe.Kind()) {
			return field + " must be at least " + fe.Param()
		}
		return field + " must be at least " + fe.Param() + " characters long"
	case "max":
		if isNumericKind(fe.Kind()) {
			return field + " must be at most " + fe.Param()
		}
		return field + " must be at most " + fe.Param() + " characters long"
	case "gte":
		return field + " must be greater than or equal to " + fe.Param()
	case "lte":
		return field + " must be less than or equal to " + fe.Param()
	case "uuid":
		return field + " must be a valid UUID"
	case "oneof":
		return field + " must be one of: " + fe.Param()
	default:
		return field + " is invalid"
	}
}

func isNumericKind(k reflect.Kind) bool {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
}
