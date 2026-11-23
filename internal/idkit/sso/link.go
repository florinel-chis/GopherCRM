package sso

import (
	"context"
	"errors"

	"github.com/florinel-chis/gophercrm/internal/idkit"
)

// signIn is what one verified callback asks the transaction for: the exact
// (issuer, subject) pair of the ID token, the link hint (entra.LinkHint, ""
// when there is none), the role the first matching mapping entry names, the
// profile of a first sign-in and the provider session id of the token (its
// sid, "" when absent).
type signIn struct {
	issuer, subject string
	linkHint        string
	roleID          uint
	profile         idkit.Profile
	sid             string
}

// signInAccount is the link rule and the session, run inside the callback's
// transaction (after the host's own first locks):
//
//   - An identity is found by its exact (issuer, subject) pair. A stored row
//     that differs byte for byte (a case- or pad-insensitive collation
//     matched a near value) is never the same identity: identity_mismatch.
//   - A known pair signs in as its account.
//   - An unknown pair with a link hint signs in as the account of the
//     identity stored under (issuer, hint), but only when that row matches
//     byte for byte and the provider created it (IdentitySourceSCIM), and
//     then links (issuer, subject) to that account.
//   - Any other unknown pair provisions an account and links the pair to it.
//   - The session is issued last.
//
// Email is never an input to an identity lookup, so it never links an
// identity to an existing account. Everything is recomputed on every run.
func (s *Service) signInAccount(ctx context.Context, in signIn) (idkit.Session, error) {
	identity, err := s.Identities.Get(ctx, in.issuer, in.subject)
	if err != nil {
		if !errors.Is(err, idkit.ErrNotFound) {
			return idkit.Session{}, err
		}
		identity = nil
	}
	if identity != nil && (identity.Issuer != in.issuer || identity.Subject != in.subject) {
		return idkit.Session{}, refuse(CodeIdentityMismatch)
	}

	// provided is the identity the provider created for this person under
	// the same issuer, found by the link hint. Anything else falls through
	// to provisioning.
	var provided *idkit.Identity
	if identity == nil && in.linkHint != "" {
		row, err := s.Identities.Get(ctx, in.issuer, in.linkHint)
		if err != nil && !errors.Is(err, idkit.ErrNotFound) {
			return idkit.Session{}, err
		}
		if err == nil && row != nil && row.Issuer == in.issuer && row.Subject == in.linkHint &&
			row.Source == idkit.IdentitySourceSCIM {
			provided = row
		}
	}

	var accountID string
	link := false
	switch {
	case identity != nil:
		if err := s.Accounts.SignIn(ctx, identity.AccountID, in.roleID); err != nil {
			return idkit.Session{}, err
		}
		accountID = identity.AccountID
	case provided != nil:
		if s.BeforeSCIMLink != nil {
			s.BeforeSCIMLink()
		}
		if err := s.Accounts.SignIn(ctx, provided.AccountID, in.roleID); err != nil {
			return idkit.Session{}, err
		}
		accountID, link = provided.AccountID, true
	default:
		created, err := s.Accounts.Provision(ctx, in.profile, in.roleID)
		if err != nil {
			return idkit.Session{}, err
		}
		accountID, link = created, true
	}

	if link {
		if err := s.Identities.Create(ctx, idkit.Identity{AccountID: accountID, Issuer: in.issuer, Subject: in.subject,
			Source: idkit.IdentitySourceOIDC}); err != nil {
			if errors.Is(err, idkit.ErrIdentityTaken) {
				refusal := refuseWith(CodeIdentityTaken, err)
				if provided != nil {
					// The pair was taken while the existing account signed
					// in: the refusal concerns that account.
					return idkit.Session{}, RefuseAccount(refusal, accountID)
				}
				return idkit.Session{}, refusal
			}
			return idkit.Session{}, err
		}
	}

	return s.Sessions.Issue(ctx, idkit.SessionRequest{
		AccountID:         accountID,
		Method:            idkit.SignInMethodOIDC,
		SignedInAt:        idkit.SystemClock{}.Now(),
		Issuer:            in.issuer,
		ProviderSessionID: in.sid,
	})
}
