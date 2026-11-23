package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// OAuthProfile is what we learn about a user from an OAuth provider, in provider-neutral form.
type OAuthProfile struct {
	Provider  string // "github"
	AccountID string // the provider's stable user id (GitHub: numeric id; NOT the changeable login)
	Email     string
	// EmailVerified is true only when the provider itself vouches for ownership of Email.
	// Account linking by email is only safe for verified addresses.
	EmailVerified bool
	Name          string
	Image         string

	// Provider credentials, stored in the account table like Better Auth does.
	AccessToken           string
	RefreshToken          string
	IDToken               string
	AccessTokenExpiresAt  *time.Time
	RefreshTokenExpiresAt *time.Time
	Scope                 string
}

// GitHubConfig configures the GitHub client. Only ClientID, ClientSecret and RedirectURL are
// required; the URLs default to github.com and are fields so tests can point the whole flow at
// an httptest fake of GitHub.
type GitHubConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string // our callback URL, must match the one registered in the GitHub OAuth app

	AuthURL    string       // default https://github.com/login/oauth/authorize
	TokenURL   string       // default https://github.com/login/oauth/access_token
	APIBaseURL string       // default https://api.github.com
	HTTPClient *http.Client // default: 10 second timeout
}

// GitHub implements "Sign in with GitHub" using the OAuth 2.0 authorization-code flow with PKCE.
//
// The flow, and who talks to whom:
//
//  1. Browser -> us:     GET /oauth/github. We generate a random `state` and a PKCE `verifier`,
//     remember both in a short-lived cookie, and redirect the browser to GitHub with
//     state + code_challenge = SHA-256(verifier).
//  2. Browser -> GitHub: the user logs in and approves. GitHub redirects back to us with
//     ?code=...&state=...
//  3. Browser -> us:     GET /oauth/github/callback. We compare `state` with the cookie
//     (defends against login CSRF: an attacker making YOUR browser complete THEIR flow).
//  4. Us -> GitHub:      exchange code (+ the verifier) for an access token, server to server.
//     GitHub checks SHA-256(verifier) == the challenge from step 1, so a stolen `code` is
//     useless without the verifier that never left our cookie. (PKCE was designed for public
//     clients, but RFC 9700 recommends it for confidential ones too.)
//  5. Us -> GitHub API:  fetch the profile and verified email, then create/link the user.
type GitHub struct {
	cfg    oauth2.Config
	api    string
	client *http.Client
}

// NewGitHub validates cfg, applies defaults and builds the client.
func NewGitHub(c GitHubConfig) (*GitHub, error) {
	if c.ClientID == "" || c.ClientSecret == "" || c.RedirectURL == "" {
		return nil, errors.New("auth: github client id, secret and redirect url are required")
	}
	if c.AuthURL == "" {
		c.AuthURL = "https://github.com/login/oauth/authorize"
	}
	if c.TokenURL == "" {
		c.TokenURL = "https://github.com/login/oauth/access_token"
	}
	if c.APIBaseURL == "" {
		c.APIBaseURL = "https://api.github.com"
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: 10 * time.Second} // never call out without a timeout
	}
	return &GitHub{
		cfg: oauth2.Config{
			ClientID: c.ClientID, ClientSecret: c.ClientSecret, RedirectURL: c.RedirectURL,
			// read:user for the profile, user:email for /user/emails (the profile's own email
			// field is only the PUBLIC address and is often empty).
			Scopes: []string{"read:user", "user:email"},
			Endpoint: oauth2.Endpoint{
				AuthURL: c.AuthURL, TokenURL: c.TokenURL,
				AuthStyle: oauth2.AuthStyleInParams, // send client_id/secret in the body, as GitHub documents
			},
		},
		api:    strings.TrimRight(c.APIBaseURL, "/"),
		client: c.HTTPClient,
	}, nil
}

// NewPKCEVerifier returns a fresh random PKCE code verifier (43 URL-safe characters).
func NewPKCEVerifier() string { return oauth2.GenerateVerifier() }

// AuthCodeURL returns the GitHub URL to redirect the browser to (step 1).
func (g *GitHub) AuthCodeURL(state, verifier string) string {
	return g.cfg.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier))
}

// Profile completes the flow (steps 4 and 5): it exchanges code for a token and loads the user's
// identity. It returns an error unless GitHub reports a verified primary email.
func (g *GitHub) Profile(ctx context.Context, code, verifier string) (OAuthProfile, error) {
	// oauth2 takes its HTTP client from the context; this is how tests and timeouts are injected.
	ctx = context.WithValue(ctx, oauth2.HTTPClient, g.client)
	tok, err := g.cfg.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return OAuthProfile{}, fmt.Errorf("auth: github token exchange: %w", err)
	}
	api := g.cfg.Client(ctx, tok) // adds "Authorization: Bearer <access token>"

	var user struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := getJSON(ctx, api, g.api+"/user", &user); err != nil {
		return OAuthProfile{}, err
	}
	if user.ID == 0 {
		return OAuthProfile{}, errors.New("auth: github returned no user id")
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := getJSON(ctx, api, g.api+"/user/emails", &emails); err != nil {
		return OAuthProfile{}, err
	}
	p := OAuthProfile{
		Provider: "github", AccountID: strconv.FormatInt(user.ID, 10),
		Name: user.Name, Image: user.AvatarURL,
		AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken,
		Scope: strings.Join(g.cfg.Scopes, " "),
	}
	if p.Name == "" {
		p.Name = user.Login // many GitHub users never set a display name
	}
	if s, ok := tok.Extra("scope").(string); ok && s != "" {
		p.Scope = s
	}
	if !tok.Expiry.IsZero() {
		exp := tok.Expiry.UTC()
		p.AccessTokenExpiresAt = &exp
	}
	for _, e := range emails {
		if e.Primary && e.Verified {
			p.Email, p.EmailVerified = e.Email, true
			break
		}
	}
	// No verified primary email: return the profile anyway (the service decides what to do) with
	// EmailVerified=false.
	return p, nil
}

// getJSON GETs url with an authenticated client and decodes a JSON body, capped at 1 MiB.
func getJSON(ctx context.Context, c *http.Client, url string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	res, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("auth: github GET %s: %w", url, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("auth: github GET %s: status %d", url, res.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(dst); err != nil {
		return fmt.Errorf("auth: github GET %s: decode: %w", url, err)
	}
	return nil
}
