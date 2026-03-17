package errors

// Authentication error constructors

// NewInvalidCredentials creates an invalid credentials error
func NewInvalidCredentials() *AppError {
	return New(CodeInvalidCredentials, "Invalid email or password")
}

// NewAccountDisabled creates an account disabled error
func NewAccountDisabled() *AppError {
	return New(CodeAccountDisabled, "Account is disabled")
}

// NewInvalidToken creates an invalid token error
func NewInvalidToken() *AppError {
	return New(CodeInvalidToken, "Invalid or malformed token")
}

// NewTokenExpired creates a token expired error
func NewTokenExpired() *AppError {
	return New(CodeTokenExpired, "Token has expired")
}

// NewInvalidAPIKey creates an invalid API key error
func NewInvalidAPIKey() *AppError {
	return New(CodeInvalidAPIKey, "Invalid API key")
}

// NewAPIKeyExpired creates an API key expired error
func NewAPIKeyExpired() *AppError {
	return New(CodeAPIKeyExpired, "API key has expired")
}

// NewUnauthorized creates an unauthorized error
func NewUnauthorized() *AppError {
	return New(CodeUnauthorized, "Unauthorized access")
}

// NewInsufficientPermissions creates an insufficient permissions error
func NewInsufficientPermissions() *AppError {
	return New(CodeInsufficientPermissions, "Insufficient permissions")
}