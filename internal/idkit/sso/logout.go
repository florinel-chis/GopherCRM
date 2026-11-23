package sso

import (
	"context"
	"errors"
	"net/url"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/florinel-chis/gophercrm/internal/idkit"
	idkitoidc "github.com/florinel-chis/gophercrm/internal/idkit/oidc"
)

// Sign-out of a single sign-on session, both ways:
//
//   - EndSessionURL is the RP-initiated half (OpenID Connect RP-Initiated
//     Logout 1.0): after the host ended its own session, the client is sent
//     to the provider's end_session_endpoint with the ID token of the
//     sign-in as id_token_hint, so the provider's session ends too.
//   - FrontChannelLogout is the provider-initiated half (OpenID Connect
//     Front-Channel Logout 1.0): the provider names its session by sid (and
//     optionally iss), and the host ends the sessions that signed in under
//     it.
//
// Neither ever logs a sid, an issuer, a hint, a URL or a claim value: only
// fixed messages, codes, counts and account ids.

// errNoSessionEnder is a wiring fault: FrontChannelLogout needs
// Options.SessionEnder.
var errNoSessionEnder = errors.New("single sign-on sign-out needs a session ender")

// signOutInfo and signOutWarn write one sign-out line with the given fields.
func (s *Service) signOutInfo(ctx context.Context, msg string, fields ...idkit.Field) {
	if s.Logger != nil {
		s.Logger.Info(ctx, msg, fields...)
	}
}

func (s *Service) signOutWarn(ctx context.Context, msg string, fields ...idkit.Field) {
	if s.Logger != nil {
		s.Logger.Warn(ctx, msg, fields...)
	}
}

// EndSessionURL returns where to send the browser of accountID after its
// session ended, so the provider's session ends too: the discovered
// end_session_endpoint, with its own query kept, plus id_token_hint,
// post_logout_redirect_uri (PostLogoutRedirectURL, when set) and client_id.
//
// It returns "" when single sign-on is disabled or not configured, the hint
// is empty or does not verify (signature, iss and aud = the configured
// client; its expiry is not checked), its exact (iss, sub) pair is linked to
// no account or to another one than accountID (compared byte for byte), or
// the provider advertises no usable end_session_endpoint. The whole call runs
// under EndSessionTimeout: a timeout or a failed discovery is "" and one
// warn line. Only a failure to read the configuration or the identity is an
// error; a caller that already ended the session treats it as "".
func (s *Service) EndSessionURL(ctx context.Context, accountID, idTokenHint string) (string, error) {
	if idTokenHint == "" {
		return "", nil
	}
	cfg, err := s.Config.Config(ctx)
	if err != nil {
		return "", err
	}
	if !cfg.Enabled || !s.ssoConfigured(cfg) {
		return "", nil
	}
	userID := idkit.Field{Key: "user_id", Value: logID(accountID)}

	ctx, cancel := context.WithTimeout(ctx, s.EndSessionTimeout)
	defer cancel()
	ctx = gooidc.ClientContext(ctx, s.client)
	discovered, err := s.providers.Get(ctx, s.providerKey(cfg))
	if err != nil {
		code := CodeDiscoveryFailed
		if ctx.Err() != nil {
			code = CodeEndSessionTimeout
		}
		s.signOutWarn(ctx, "Single sign-on sign-out without the provider's end-session URL", userID,
			idkit.Field{Key: "error_code", Value: code})
		return "", nil
	}
	token, err := discovered.VerifyHint(ctx, cfg.ClientID, idTokenHint)
	if err != nil {
		if ctx.Err() != nil {
			s.signOutWarn(ctx, "Single sign-on sign-out without the provider's end-session URL", userID,
				idkit.Field{Key: "error_code", Value: CodeEndSessionTimeout})
			return "", nil
		}
		s.signOutInfo(ctx, "Single sign-on sign-out without the provider's end-session URL", userID,
			idkit.Field{Key: "error_code", Value: CodeHintInvalid})
		return "", nil
	}
	identity, err := s.Identities.Get(ctx, token.Issuer, token.Subject)
	if err != nil {
		if errors.Is(err, idkit.ErrNotFound) {
			s.signOutInfo(ctx, "Single sign-on sign-out without the provider's end-session URL", userID,
				idkit.Field{Key: "error_code", Value: CodeHintUnlinked})
			return "", nil
		}
		return "", err
	}
	if identity == nil || identity.Issuer != token.Issuer || identity.Subject != token.Subject {
		s.signOutInfo(ctx, "Single sign-on sign-out without the provider's end-session URL", userID,
			idkit.Field{Key: "error_code", Value: CodeHintUnlinked})
		return "", nil
	}
	if identity.AccountID != accountID {
		s.signOutInfo(ctx, "Single sign-on sign-out without the provider's end-session URL", userID,
			idkit.Field{Key: "error_code", Value: CodeHintOtherAccount})
		return "", nil
	}
	endpoint := discovered.EndSessionEndpoint()
	if endpoint == "" {
		s.signOutInfo(ctx, "Single sign-on sign-out without the provider's end-session URL", userID,
			idkit.Field{Key: "error_code", Value: CodeEndSessionUnsupported})
		return "", nil
	}
	target, err := url.Parse(endpoint)
	if err != nil {
		return "", nil
	}
	query := target.Query()
	query.Set("id_token_hint", idTokenHint)
	if s.PostLogoutRedirectURL != "" {
		query.Set("post_logout_redirect_uri", s.PostLogoutRedirectURL)
	}
	query.Set("client_id", cfg.ClientID)
	target.RawQuery = query.Encode()
	s.signOutInfo(ctx, "Single sign-on sign-out continues at the provider", userID)
	return target.String(), nil
}

// FrontChannelLogout ends the host's sessions that signed in under the
// provider session sid of the configured issuer and returns how many
// accounts it ended. iss is optional (Entra ID sends only sid); when it is
// sent it must equal the configured issuer byte for byte, or nothing ends.
// An empty or over-long sid ends nothing. It is ErrDisabled when single
// sign-on is off and ErrUnavailable (not_configured) when no issuer or
// client is configured. It runs outside a transaction.
func (s *Service) FrontChannelLogout(ctx context.Context, iss, sid string) (int, error) {
	cfg, err := s.usableConfig(ctx)
	if err != nil {
		return 0, err
	}
	if !s.ssoConfigured(cfg) {
		s.signOutWarn(ctx, "Single sign-on is enabled but not configured", idkit.Field{Key: "error_code", Value: CodeNotConfigured})
		return 0, unavailable(CodeNotConfigured)
	}
	if iss != "" && iss != cfg.IssuerURL {
		s.signOutInfo(ctx, "Front-channel sign-out ignored", idkit.Field{Key: "error_code", Value: CodeIssuerMismatch})
		return 0, nil
	}
	if sid == "" || len(sid) > idkitoidc.SessionIDMaxBytes {
		return 0, nil
	}
	if s.SessionEnder == nil {
		return 0, errNoSessionEnder
	}
	ended, err := s.SessionEnder.EndProviderSession(ctx, cfg.IssuerURL, sid)
	if err != nil {
		return 0, err
	}
	s.signOutInfo(ctx, "Front-channel sign-out handled", idkit.Field{Key: "ended", Value: ended})
	return ended, nil
}
