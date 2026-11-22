// Copyright (C) 2026 Brainslad <https://github.com/brainslad>
// SPDX-License-Identifier: GPL-3.0-or-later
// Part of MonitorIQ. Licensed under the GNU GPL v3 or later with an author
// attribution term (section 7(b)); see LICENSE and NOTICE.

package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// OIDCConfig configures single sign-on with an OpenID Connect provider:
// Microsoft Entra ID (Azure AD), Google, Okta, Keycloak, Authentik, ADFS…
type OIDCConfig struct {
	Enabled        bool   `json:"enabled"`
	ButtonLabel    string `json:"button_label"`
	Issuer         string `json:"issuer"` // e.g. https://login.microsoftonline.com/<tenant-id>/v2.0
	ClientID       string `json:"client_id"`
	ClientSecret   string `json:"client_secret"`
	Scopes         string `json:"scopes"`
	UsernameClaim  string `json:"username_claim"`  // preferred_username, email, upn, sub…
	AllowedDomains string `json:"allowed_domains"` // e-mail domains, comma separated; empty = any
	GroupsClaim    string `json:"groups_claim"`
	RequiredGroups string `json:"required_groups"` // any of these (IDs or names); empty = no check
	AutoCreate     bool   `json:"auto_create"`
	// HideLocalLogin shows only the SSO button on the sign-in page (the
	// username/password form stays reachable at /login?local=1).
	HideLocalLogin bool `json:"hide_local_login"`
}

// AzureIssuer returns the Entra ID issuer URL for a tenant.
func AzureIssuer(tenant string) string {
	return "https://login.microsoftonline.com/" + strings.TrimSpace(tenant) + "/v2.0"
}

func (c *OIDCConfig) Validate() error {
	if !validURL(c.Issuer, "https") && !strings.HasPrefix(c.Issuer, "http://127.0.0.1") && !strings.HasPrefix(c.Issuer, "http://localhost") {
		return errors.New("the issuer must be an https:// URL")
	}
	if strings.TrimSpace(c.ClientID) == "" {
		return errors.New("the client ID is required")
	}
	if strings.TrimSpace(c.UsernameClaim) == "" {
		return errors.New("the username claim is required")
	}
	return nil
}

func (c *OIDCConfig) Label() string {
	if strings.TrimSpace(c.ButtonLabel) == "" {
		return "Sign in with SSO"
	}
	return c.ButtonLabel
}

func (c *OIDCConfig) scopes() []string {
	sc := strings.Fields(strings.ReplaceAll(c.Scopes, ",", " "))
	has := false
	for _, s := range sc {
		if s == oidc.ScopeOpenID {
			has = true
		}
	}
	if !has {
		sc = append([]string{oidc.ScopeOpenID}, sc...)
	}
	return sc
}

// OIDC holds discovered provider metadata, refreshed when the configuration
// changes.
type OIDC struct {
	HTTPClient *http.Client

	mu       sync.Mutex
	issuer   string
	provider *oidc.Provider
	fetched  time.Time
}

func (o *OIDC) ctx(ctx context.Context) context.Context {
	if o.HTTPClient != nil {
		return oidc.ClientContext(ctx, o.HTTPClient)
	}
	return ctx
}

func (o *OIDC) providerFor(ctx context.Context, issuer string) (*oidc.Provider, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.provider != nil && o.issuer == issuer && time.Since(o.fetched) < 24*time.Hour {
		return o.provider, nil
	}
	dctx, cancel := context.WithTimeout(o.ctx(ctx), 15*time.Second)
	defer cancel()
	p, err := oidc.NewProvider(dctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("cannot load the provider configuration from %s: %w", issuer, err)
	}
	o.issuer, o.provider, o.fetched = issuer, p, time.Now()
	return p, nil
}

func (o *OIDC) oauth(ctx context.Context, c *OIDCConfig, redirectURL string) (*oauth2.Config, *oidc.Provider, error) {
	p, err := o.providerFor(ctx, strings.TrimRight(c.Issuer, "/"))
	if err != nil {
		return nil, nil, err
	}
	return &oauth2.Config{
		ClientID:     c.ClientID,
		ClientSecret: c.ClientSecret,
		Endpoint:     p.Endpoint(),
		RedirectURL:  redirectURL,
		Scopes:       c.scopes(),
	}, p, nil
}

// Flow is the per-login state kept in a signed cookie between the redirect
// to the provider and the callback.
type Flow struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Next     string `json:"x"`
	Expires  int64  `json:"e"`
}

// AuthURL starts a login and returns the URL to redirect the browser to.
func (o *OIDC) AuthURL(ctx context.Context, c *OIDCConfig, redirectURL, next string) (string, *Flow, error) {
	oc, _, err := o.oauth(ctx, c, redirectURL)
	if err != nil {
		return "", nil, err
	}
	f := &Flow{State: randomString(24), Nonce: randomString(24), Verifier: oauth2.GenerateVerifier(),
		Next: next, Expires: time.Now().Add(10 * time.Minute).Unix()}
	u := oc.AuthCodeURL(f.State, oidc.Nonce(f.Nonce), oauth2.S256ChallengeOption(f.Verifier))
	return u, f, nil
}

// Exchange completes a login: it redeems the code, verifies the ID token and
// applies the domain and group restrictions.
func (o *OIDC) Exchange(ctx context.Context, c *OIDCConfig, redirectURL string, f *Flow, code string) (*Identity, error) {
	if time.Now().Unix() > f.Expires {
		return nil, errors.New("the sign-in took too long, please try again")
	}
	oc, p, err := o.oauth(ctx, c, redirectURL)
	if err != nil {
		return nil, err
	}
	xctx, cancel := context.WithTimeout(o.ctx(ctx), 15*time.Second)
	defer cancel()
	tok, err := oc.Exchange(xctx, code, oauth2.VerifierOption(f.Verifier))
	if err != nil {
		return nil, fmt.Errorf("code exchange failed: %w", err)
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok || raw == "" {
		return nil, errors.New("the provider did not return an ID token")
	}
	idt, err := p.Verifier(&oidc.Config{ClientID: c.ClientID}).Verify(xctx, raw)
	if err != nil {
		return nil, fmt.Errorf("invalid ID token: %w", err)
	}
	if idt.Nonce != f.Nonce {
		return nil, errors.New("invalid ID token nonce")
	}
	var claims map[string]any
	if err := idt.Claims(&claims); err != nil {
		return nil, err
	}
	return c.identityFromClaims(idt.Issuer, idt.Subject, claims)
}

func claimString(claims map[string]any, name string) string {
	if v, ok := claims[name].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func claimList(claims map[string]any, name string) []string {
	switch v := claims[name].(type) {
	case string:
		return SplitList(v)
	case []any:
		var out []string
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func (c *OIDCConfig) identityFromClaims(issuer, subject string, claims map[string]any) (*Identity, error) {
	id := &Identity{
		Source:      "oidc",
		ExternalID:  issuer + "|" + subject,
		Username:    claimString(claims, c.UsernameClaim),
		DisplayName: claimString(claims, "name"),
		Email:       claimString(claims, "email"),
	}
	if id.Email == "" && strings.Contains(claimString(claims, "preferred_username"), "@") {
		id.Email = claimString(claims, "preferred_username")
	}
	if id.Username == "" {
		return nil, fmt.Errorf("the ID token has no %q claim (check the username claim setting)", c.UsernameClaim)
	}
	if domains := SplitList(c.AllowedDomains); len(domains) > 0 {
		addr := id.Email
		if addr == "" {
			addr = id.Username
		}
		_, dom, _ := strings.Cut(strings.ToLower(addr), "@")
		allowed := false
		for _, d := range domains {
			if dom != "" && dom == strings.ToLower(strings.TrimPrefix(d, "@")) {
				allowed = true
			}
		}
		if !allowed {
			return nil, ErrDenied
		}
	}
	if required := SplitList(c.RequiredGroups); len(required) > 0 {
		claim := c.GroupsClaim
		if claim == "" {
			claim = "groups"
		}
		if _, overage := claims["_claim_names"]; overage && claims[claim] == nil {
			return nil, errors.New("the user belongs to too many groups to be listed in the token; in Entra ID set \"groupMembershipClaims\" to ApplicationGroup and assign the groups to the application")
		}
		member := false
		for _, g := range claimList(claims, claim) {
			for _, r := range required {
				if strings.EqualFold(g, r) {
					member = true
				}
			}
		}
		if !member {
			return nil, ErrDenied
		}
	}
	return id, nil
}
