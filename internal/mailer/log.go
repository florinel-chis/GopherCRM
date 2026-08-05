package mailer

// LogMailer is the development fallback used when no SMTP host is configured.
// It logs that a reset mail would have been sent — recipient plus the link
// with the token value redacted. The full link (and thus the raw token) is
// deliberately never logged: without a transport there is simply no delivery,
// which keeps the reset flow inert but observable in development.
type LogMailer struct{}

func NewLogMailer() *LogMailer {
	return &LogMailer{}
}

func (m *LogMailer) SendPasswordReset(to, resetURL string) error {
	log().
		WithField("to", to).
		WithField("reset_url", redactResetURL(resetURL)).
		Info("SMTP not configured; password reset email logged instead of sent")
	return nil
}
