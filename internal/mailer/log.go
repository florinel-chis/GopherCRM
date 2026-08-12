package mailer

// LogMailer is the development fallback used when no SMTP host is configured.
// It logs that a mail would have been sent — recipient plus the link with the
// token value redacted, or for generic mail the subject alone. Raw tokens are
// deliberately never logged: without a transport there is simply no delivery,
// which keeps token-bearing flows inert but observable in development.
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

// Send records the delivery of a generic message. Only the recipient and the
// subject are logged — bodies may embed single-use links.
func (m *LogMailer) Send(to, subject, _ string) error {
	log().
		WithField("to", to).
		WithField("subject", subject).
		Info("SMTP not configured; email logged instead of sent")
	return nil
}
