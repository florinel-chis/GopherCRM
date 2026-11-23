package sso

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/florinel-chis/gophercrm/internal/idkit"
	"github.com/florinel-chis/gophercrm/internal/idkit/entra"
	idkitoidc "github.com/florinel-chis/gophercrm/internal/idkit/oidc"
	"github.com/florinel-chis/gophercrm/internal/idkit/rolemap"
)

// callbackFacts is what a callback learned before it ended, for the audit
// event: the claimed attempt, the verified identity, and the claim values
// the mapping used.
type callbackFacts struct {
	// attemptID is the claimed attempt, "" when the claim did not succeed.
	attemptID string
	// verified is set once the ID token verified and its nonce matched;
	// issuer and subject are its iss and sub.
	verified        bool
	issuer, subject string
	// groups and roles are nil until they were read and well formed; groups
	// is the resolved list once a Graph or userinfo resolution ran.
	groups, roles []string
}

// claimValues is a read claim array for the audit event: never nil, so an
// absent claim is recorded as [] and not as nothing.
func claimValues(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// callback finishes the sign-in and also returns what it learned: the id of
// the attempt it claimed ("" when the claim did not succeed), for the log
// line of Callback, and the verified identity and claim values, for the
// audit event.
func (s *Service) callback(ctx context.Context, code, state, binding string) (idkit.Session, callbackFacts, error) {
	var facts callbackFacts
	cfg, err := s.usableConfig(ctx)
	if err != nil {
		return idkit.Session{}, facts, err
	}

	// The claim is its own committed statement, outside any transaction: a
	// state is spent whatever happens next, so it can never be replayed; the
	// returned attempt still holds the verifier and nonce in memory.
	attempt, err := s.Attempts.Claim(ctx, s.StateHash(state), s.now().UTC())
	if err != nil {
		if errors.Is(err, idkit.ErrNotFound) {
			return idkit.Session{}, facts, refuse(CodeStateInvalid)
		}
		return idkit.Session{}, facts, fmt.Errorf("claim the single sign-on attempt: %w", err)
	}
	facts.attemptID = attempt.ID
	// The binding the browser holds is the raw state, and the attempt was
	// found by the state's hash: a binding equal to the posted state is
	// bound to this attempt. Checked after the claim, so a refused callback
	// still spends its attempt and its audit event names it.
	if binding == "" {
		return idkit.Session{}, facts, refuse(CodeBindingMissing)
	}
	if !idkitoidc.BindingMatches(binding, state) {
		return idkit.Session{}, facts, refuse(CodeBindingMismatch)
	}
	if attempt.Issuer != cfg.IssuerURL {
		return idkit.Session{}, facts, refuse(CodeIssuerChanged)
	}
	if !s.ssoConfigured(cfg) {
		s.warn(ctx, attempt.ID, "Single sign-on is enabled but not configured", idkit.Field{Key: "error_code", Value: CodeNotConfigured})
		return idkit.Session{}, facts, unavailable(CodeNotConfigured)
	}
	ctx, discovered, err := s.discover(ctx, cfg, attempt.ID)
	if err != nil {
		return idkit.Session{}, facts, err
	}
	provider := discovered.Provider

	token, err := s.exchangeCode(ctx, provider, cfg, code, attempt.Verifier)
	if err != nil {
		s.warn(ctx, attempt.ID, "Single sign-on code exchange failed", idkit.Field{Key: "error_code", Value: CodeTokenExchangeFailed})
		return idkit.Session{}, facts, unavailable(CodeTokenExchangeFailed)
	}
	rawIDToken, _ := token.Extra("id_token").(string)
	if rawIDToken == "" {
		return idkit.Session{}, facts, refuse(CodeIDTokenMissing)
	}
	idToken, err := discovered.Verifier(cfg.ClientID).Verify(ctx, rawIDToken)
	if err != nil {
		return idkit.Session{}, facts, refuse(CodeIDTokenInvalid)
	}
	if !idkitoidc.NonceMatches(idToken.Nonce, attempt.Nonce) {
		return idkit.Session{}, facts, refuse(CodeNonceMismatch)
	}
	facts.verified, facts.issuer, facts.subject = true, idToken.Issuer, idToken.Subject

	var claims idkitoidc.Claims
	if err := idToken.Claims(&claims); err != nil {
		return idkit.Session{}, facts, refuse(CodeClaimsInvalid)
	}
	// Distributed claims: an issuer that moved groups out of the token (a
	// group overage). Without a groups mapping the overage does not matter;
	// on Entra ID the groups are read from Microsoft Graph below, after every
	// other check; any other issuer fails closed. _claim_sources is never
	// read. A groups mapping and a token without a groups claim (and no
	// overage) read the groups from the userinfo endpoint instead.
	resolveGroups := false
	if len(claims.ClaimNames) > 0 {
		var names map[string]json.RawMessage
		if err := json.Unmarshal(claims.ClaimNames, &names); err != nil {
			return idkit.Session{}, facts, refuse(CodeClaimsInvalid)
		}
		if _, overage := names[rolemap.ClaimGroups]; overage && rolemap.UsesGroups(cfg.RoleMapping) {
			if !entra.IsIssuer(cfg.IssuerURL, s.EntraIssuerHost) {
				return idkit.Session{}, facts, refuse(CodeGroupOverage)
			}
			resolveGroups = true
		}
	}
	groups, ok := idkitoidc.StringArray(claims.Groups)
	if !ok {
		return idkit.Session{}, facts, refuse(CodeGroupsMalformed)
	}
	roles, ok := idkitoidc.StringArray(claims.Roles)
	if !ok {
		return idkit.Session{}, facts, refuse(CodeRolesMalformed)
	}
	facts.roles = claimValues(roles)
	if !resolveGroups && !(rolemap.UsesGroups(cfg.RoleMapping) && len(claims.Groups) == 0) {
		facts.groups = claimValues(groups)
	}
	subject := idToken.Subject
	if !idkitoidc.ValidSubject(subject) {
		return idkit.Session{}, facts, refuse(CodeSubjectInvalid)
	}
	if len(idToken.Issuer) > s.IssuerMaxBytes {
		return idkit.Session{}, facts, refuse(CodeIssuerInvalid)
	}
	if resolveGroups {
		resolver := entra.GroupResolver{BaseURL: s.GraphBaseURL, Timeout: s.GraphTimeout, Client: idkitoidc.NewHTTPClient(s.RootCAs)}
		resolved, err := resolver.MemberObjects(ctx, token.AccessToken, cfg.GraphMaxPages)
		if err != nil {
			return idkit.Session{}, facts, refuse(CodeGroupsUnavailable)
		}
		groups = resolved
		facts.groups = claimValues(groups)
	} else if rolemap.UsesGroups(cfg.RoleMapping) && len(claims.Groups) == 0 {
		resolver := UserInfoResolver{Timeout: s.UserInfoTimeout, Client: idkitoidc.NewHTTPClient(s.RootCAs)}
		resolved, err := resolver.Groups(ctx, provider, token.AccessToken, subject)
		if err != nil {
			if errors.Is(err, ErrUserInfoSubject) {
				return idkit.Session{}, facts, refuse(CodeUserInfoSubjectMismatch)
			}
			return idkit.Session{}, facts, refuse(CodeGroupsUnavailable)
		}
		groups = resolved
		facts.groups = claimValues(groups)
	}

	roleID, matched := rolemap.Match(cfg.RoleMapping, groups, roles)
	if !matched {
		return idkit.Session{}, facts, refuse(CodeNoMappingMatch)
	}

	// The oid links to an identity the provider created only under an Entra
	// ID issuer and only when the token's tenant is the configured issuer's
	// tenant, compared byte for byte; a non-string claim counts as absent.
	linkHint := entra.LinkHint(cfg.IssuerURL, s.EntraIssuerHost, idkitoidc.String(claims.TID), idkitoidc.String(claims.OID))
	firstName, lastName := idkitoidc.ProfileNames(idkitoidc.String(claims.GivenName), idkitoidc.String(claims.FamilyName), idkitoidc.String(claims.Name))
	in := signIn{
		issuer:   idToken.Issuer,
		subject:  subject,
		linkHint: linkHint,
		roleID:   roleID,
		profile:  idkit.Profile{Email: idkitoidc.String(claims.Email), GivenName: firstName, FamilyName: lastName},
		sid:      idkitoidc.SessionID(claims.SID),
	}

	// One transaction: the link rule, the account sign-in or provisioning,
	// the identity link and the session. The runner may replay fn; every run
	// starts from in and keeps nothing of an earlier one.
	var session idkit.Session
	err = s.Tx.WithTransaction(ctx, func(ctx context.Context) error {
		issued, err := s.signInAccount(ctx, in)
		if err != nil {
			return err
		}
		session = issued
		return nil
	})
	if err != nil {
		return idkit.Session{}, facts, err
	}
	// The raw ID token goes back to the client only, as the hint of its
	// sign-out; no store ever sees it.
	session.IDToken = rawIDToken
	return session, facts, nil
}
