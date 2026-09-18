package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	sessionCookieName = "radar_oidc_session"
	preAuthTTL        = 10 * time.Minute
	sessionTTL        = 8 * time.Hour
	defaultReturnTo   = "/admin"
)

// Config holds the OIDC authorization-code-flow configuration. An empty
// Issuer disables OIDC entirely.
type Config struct {
	Issuer         string
	ClientID       string
	ClientSecret   string
	RedirectURL    string
	Scopes         []string // empty → {"openid","profile","email"}
	AllowedEmails  []string
	AllowedDomains []string
	AllowedSubs    []string
	TrustProxy     bool
}

// Enabled reports whether OIDC is configured.
func (c Config) Enabled() bool { return c.Issuer != "" }

// Auth implements the OIDC authorization-code flow backed by SQLite sessions.
// It composes the oauth2 config, the ID token verifier, the session store,
// the allowlist, and the proxy trust flag for Secure cookie decisions.
type Auth struct {
	oauth      oauth2.Config
	verifier   *oidc.IDTokenVerifier
	store      *sessionStore
	allow      Allowlist
	trustProxy bool
	client     *http.Client // optional HTTP client from the New context (test hook)
}

// New prepares the session table, sweeps expired rows, and runs OIDC
// discovery. When the config is disabled (empty Issuer) the returned Auth
// has Enabled() == false and no discovery is performed.
func New(ctx context.Context, cfg Config, db *sql.DB) (*Auth, error) {
	store := &sessionStore{db: db}
	if err := store.migrate(ctx); err != nil {
		return nil, fmt.Errorf("create oidc sessions table: %w", err)
	}
	if err := store.Sweep(ctx); err != nil {
		return nil, fmt.Errorf("sweep oidc sessions: %w", err)
	}
	a := &Auth{
		store:      store,
		allow:      NewAllowlist(cfg.AllowedEmails, cfg.AllowedDomains, cfg.AllowedSubs),
		trustProxy: cfg.TrustProxy,
	}
	if client, ok := ctx.Value(oauth2.HTTPClient).(*http.Client); ok && client != nil {
		a.client = client
	}
	if !cfg.Enabled() {
		return a, nil
	}
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery failed: %w", err)
	}
	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID, oidc.ScopeProfile, oidc.ScopeEmail}
	}
	a.oauth = oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  cfg.RedirectURL,
		Scopes:       scopes,
		Endpoint:     provider.Endpoint(),
	}
	a.verifier = provider.Verifier(&oidc.Config{ClientID: cfg.ClientID})
	return a, nil
}

// Enabled reports whether the flow is usable. It is nil-safe.
func (a *Auth) Enabled() bool { return a != nil && a.oauth.Endpoint.AuthURL != "" }

// Session returns the authenticated session for the request, or nil when the
// cookie is missing, the row is unknown, expired, or still in the pre-auth
// phase. It is nil-safe.
func (a *Auth) Session(r *http.Request) *Session {
	if a == nil {
		return nil
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil
	}
	row, err := a.store.Get(r.Context(), cookie.Value)
	if err != nil {
		return nil
	}
	if row.UserSub == "" || row.ExpiresAt.Before(time.Now()) {
		return nil
	}
	return &Session{
		UserSub:   row.UserSub,
		Email:     row.Email,
		Name:      row.Name,
		CSRFToken: row.CSRFToken,
		ExpiresAt: row.ExpiresAt,
	}
}

func (a *Auth) cookieSecure(r *http.Request) bool {
	return r.TLS != nil || (a.trustProxy && r.Header.Get("X-Forwarded-Proto") == "https")
}

func (a *Auth) setSessionCookie(w http.ResponseWriter, r *http.Request, value string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   a.cookieSecure(r),
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
	})
}

func (a *Auth) clearCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   a.cookieSecure(r),
		Expires:  time.Unix(1, 0),
		MaxAge:   -1,
	})
}

// sanitizeReturnTo accepts only same-site paths: strings starting with "/"
// but not "//" (which browsers treat as protocol-relative absolute URLs), and
// never any value containing a backslash (browsers normalize "/\evil.com"
// into a protocol-relative external URL).
func sanitizeReturnTo(value string) string {
	if strings.Contains(value, `\`) {
		return defaultReturnTo
	}
	if strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//") {
		return value
	}
	return defaultReturnTo
}

func (a *Auth) disabled(w http.ResponseWriter) {
	http.Error(w, "auth disabled", http.StatusServiceUnavailable)
}

// requestContext re-attaches the HTTP client captured at construction time so
// handler-level provider calls (token exchange, JWKS) use the same client as
// discovery. Production contexts carry no custom client, making this a no-op.
func (a *Auth) requestContext(r *http.Request) context.Context {
	if a.client == nil {
		return r.Context()
	}
	return context.WithValue(r.Context(), oauth2.HTTPClient, a.client)
}

// LoginHandler handles GET /oidc/login: it creates a pre-auth session row,
// sets the session cookie, and redirects to the provider's authorize URL
// with PKCE (S256) and a nonce.
func (a *Auth) LoginHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.Enabled() {
			a.disabled(w)
			return
		}
		ctx := a.requestContext(r)
		returnTo := sanitizeReturnTo(r.URL.Query().Get("return_to"))
		state, nonce := rand.Text(), rand.Text()
		verifier := oauth2.GenerateVerifier()
		now := time.Now()
		row := sessionRow{
			ID:           rand.Text(),
			State:        state,
			Nonce:        nonce,
			CodeVerifier: verifier,
			ReturnTo:     returnTo,
			CSRFToken:    rand.Text(),
			CreatedAt:    now,
			ExpiresAt:    now.Add(preAuthTTL),
		}
		if err := a.store.Create(ctx, row); err != nil {
			slog.ErrorContext(ctx, "oidc login failed", "error", err)
			http.Error(w, "could not start login", http.StatusInternalServerError)
			return
		}
		a.setSessionCookie(w, r, row.ID, row.ExpiresAt)
		http.Redirect(w, r, a.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), http.StatusFound)
	}
}

// CallbackHandler handles GET /oidc/callback: it validates the anti-forgery
// state, exchanges the code with PKCE, verifies the ID token (including the
// nonce, which go-oidc does not check), applies the allowlist, and rotates
// into an authenticated session row with a fresh cookie.
func (a *Auth) CallbackHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.Enabled() {
			a.disabled(w)
			return
		}
		ctx := a.requestContext(r)
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil {
			a.clearCookie(w, r)
			http.Error(w, "stale session", http.StatusBadRequest)
			return
		}
		row, err := a.store.Get(ctx, cookie.Value)
		if err != nil {
			a.clearCookie(w, r)
			http.Error(w, "stale session", http.StatusBadRequest)
			return
		}
		if row.ExpiresAt.Before(time.Now()) {
			_ = a.store.Delete(ctx, row.ID)
			a.clearCookie(w, r)
			http.Error(w, "stale session", http.StatusBadRequest)
			return
		}
		// Authenticated rows must never be consumed here. SameSite=Lax makes a
		// cross-site top-level GET carry the session cookie, so an attacker
		// could otherwise lure the signed-in operator to /oidc/callback and
		// delete their row below (forced logout). Reject without touching the
		// row or the cookie.
		if row.UserSub != "" {
			http.Error(w, "stale session", http.StatusBadRequest)
			return
		}
		// Pre-auth rows only: a missing state collapses to "" == "" and must
		// be rejected explicitly. On mismatch, let the row expire via Sweep
		// instead of deleting it, and clear the stale cookie client-side.
		state := r.URL.Query().Get("state")
		if state == "" || subtle.ConstantTimeCompare([]byte(state), []byte(row.State)) != 1 {
			a.clearCookie(w, r)
			http.Error(w, "stale session", http.StatusBadRequest)
			return
		}
		if r.URL.Query().Get("error") != "" {
			slog.WarnContext(ctx, "oidc authorization failed", "error", r.URL.Query().Get("error"), "error_description", r.URL.Query().Get("error_description"))
			_ = a.store.Delete(ctx, row.ID)
			a.clearCookie(w, r)
			http.Error(w, "authorization failed", http.StatusUnauthorized)
			return
		}
		// The pre-auth row is single-use: delete it before any further work.
		if err := a.store.Delete(ctx, row.ID); err != nil {
			slog.ErrorContext(ctx, "oidc callback failed", "error", err)
			http.Error(w, "authorization failed", http.StatusUnauthorized)
			return
		}
		tok, err := a.oauth.Exchange(ctx, r.URL.Query().Get("code"), oauth2.VerifierOption(row.CodeVerifier))
		if err != nil {
			slog.ErrorContext(ctx, "oidc token exchange failed", "error", err)
			http.Error(w, "authorization failed", http.StatusUnauthorized)
			return
		}
		rawID, ok := tok.Extra("id_token").(string)
		if !ok || rawID == "" {
			slog.ErrorContext(ctx, "oidc token response has no id_token")
			http.Error(w, "authorization failed", http.StatusUnauthorized)
			return
		}
		idt, err := a.verifier.Verify(ctx, rawID)
		if err != nil {
			slog.ErrorContext(ctx, "oidc id token verification failed", "error", err)
			http.Error(w, "authorization failed", http.StatusUnauthorized)
			return
		}
		// go-oidc does not verify the nonce; check it here.
		if idt.Nonce == "" || subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(row.Nonce)) != 1 {
			slog.ErrorContext(ctx, "oidc id token nonce mismatch")
			http.Error(w, "authorization failed", http.StatusUnauthorized)
			return
		}
		var claims struct {
			Sub           string `json:"sub"`
			Email         string `json:"email"`
			EmailVerified bool   `json:"email_verified"`
			Name          string `json:"name"`
		}
		if err := idt.Claims(&claims); err != nil {
			slog.ErrorContext(ctx, "oidc id token claims decode failed", "error", err)
			http.Error(w, "authorization failed", http.StatusUnauthorized)
			return
		}
		if !a.allow.Permits(claims.Sub, claims.Email, claims.EmailVerified) {
			slog.WarnContext(ctx, "oidc login denied", "sub", claims.Sub, "email", claims.Email)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		now := time.Now()
		authRow := sessionRow{
			ID:        rand.Text(),
			CSRFToken: rand.Text(),
			UserSub:   claims.Sub,
			Email:     claims.Email,
			Name:      claims.Name,
			IDToken:   rawID,
			ReturnTo:  row.ReturnTo,
			CreatedAt: now,
			ExpiresAt: now.Add(sessionTTL),
		}
		if err := a.store.Create(ctx, authRow); err != nil {
			slog.ErrorContext(ctx, "oidc callback failed", "error", err)
			http.Error(w, "could not complete login", http.StatusInternalServerError)
			return
		}
		a.setSessionCookie(w, r, authRow.ID, authRow.ExpiresAt)
		http.Redirect(w, r, authRow.ReturnTo, http.StatusFound)
	}
}

// LogoutHandler handles POST /admin/logout: it deletes the session row, if
// any, clears the cookie, and redirects to the admin page.
func (a *Auth) LogoutHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if a != nil {
			ctx := a.requestContext(r)
			if cookie, err := r.Cookie(sessionCookieName); err == nil {
				if row, err := a.store.Get(ctx, cookie.Value); err == nil {
					_ = a.store.Delete(ctx, row.ID)
				}
			}
			a.clearCookie(w, r)
		}
		http.Redirect(w, r, "/admin", http.StatusFound)
	}
}
