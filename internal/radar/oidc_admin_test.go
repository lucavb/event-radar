package radar

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/lucabecker/event-radar/internal/auth"
	"github.com/lucabecker/event-radar/internal/auth/testidp"
)

func newOIDCApp(t *testing.T, mutate func(idp *testidp.IDP, config *Config)) (*Radar, *SQLiteStore, *testidp.IDP, http.Handler) {
	t.Helper()
	idp := testidp.New(t)
	store, err := OpenStore(filepath.Join(t.TempDir(), "radar.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	config := Config{
		Branding: Branding{
			AppName:        "Event Radar",
			CalendarProdID: "-//Event Radar//EN",
			Timezone:       "UTC",
		},
		Runtime: Runtime{
			DatabasePath:      filepath.Join(t.TempDir(), "unused.db"),
			FeedToken:         "test-feed-token",
			ListenAddress:     "127.0.0.1:0",
			SyncInterval:      time.Hour,
			HTTPTimeout:       time.Second,
			AdminToken:        "admin-token",
			OIDCIssuer:        idp.Base,
			OIDCClientID:      "radar-test",
			OIDCClientSecret:  "radar-secret",
			OIDCRedirectURL:   "https://radar.test/oidc/callback",
			OIDCAllowedEmails: []string{"user@example.test"},
		},
		Observability: Observability{TracingEnabled: ""},
		Sources:       Sources{ICSFeeds: []ICSFeedConfig{{Name: "feed", URL: "https://example.test/events.ics"}}},
	}
	if mutate != nil {
		mutate(idp, &config)
	}
	authenticator, err := auth.New(idp.Context(context.Background()), config.AuthConfig(), store.DB())
	if err != nil {
		t.Fatal(err)
	}
	app := New(config, store, nil, nil)
	app.AttachAuth(authenticator)
	return app, store, idp, app.Handler()
}

// adminCookie extracts the OIDC session cookie from a response.
func adminCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == "radar_oidc_session" {
			return cookie
		}
	}
	t.Fatal("no radar_oidc_session cookie in response")
	return nil
}

// completeLogin drives login → fake authorize → callback through the mux and
// returns the rotated session cookie.
func completeLogin(t *testing.T, handler http.Handler, idp *testidp.IDP) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/oidc/login", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("login status = %d, body %q", rec.Code, rec.Body.String())
	}
	preAuthCookie := adminCookie(t, rec)
	authorizeResponse, err := idp.Client().Get(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = authorizeResponse.Body.Close() }()
	if authorizeResponse.StatusCode != http.StatusFound {
		t.Fatalf("authorize status = %d", authorizeResponse.StatusCode)
	}
	target, err := url.Parse(authorizeResponse.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if target.Query().Get("code") == "" {
		t.Fatalf("authorize redirect = %q", authorizeResponse.Header.Get("Location"))
	}
	rec = httptest.NewRecorder()
	callbackRequest := httptest.NewRequest(http.MethodGet, "/oidc/callback?"+target.RawQuery, nil)
	callbackRequest.AddCookie(preAuthCookie)
	handler.ServeHTTP(rec, callbackRequest)
	if rec.Code != http.StatusFound {
		t.Fatalf("callback status = %d, body %q", rec.Code, rec.Body.String())
	}
	if location := rec.Header().Get("Location"); location != "/admin" {
		t.Fatalf("callback location = %q, want /admin", location)
	}
	return adminCookie(t, rec)
}

var csrfPattern = regexp.MustCompile(`name=csrf value="([^"]+)"`)

func TestOIDCDisabledAdminStillTokenGated(t *testing.T) {
	idp := testidp.New(t)
	store, err := OpenStore(filepath.Join(t.TempDir(), "radar.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	config := Config{
		Branding: Branding{AppName: "Event Radar", CalendarProdID: "-//Event Radar//EN", Timezone: "UTC"},
		Runtime: Runtime{
			DatabasePath:  filepath.Join(t.TempDir(), "unused.db"),
			FeedToken:     "test-feed-token",
			ListenAddress: "127.0.0.1:0",
			SyncInterval:  time.Hour,
			HTTPTimeout:   time.Second,
			AdminToken:    "admin-token",
			OIDCIssuer:    idp.Base, // configured but deliberately not attached
		},
	}
	app := New(config, store, nil, nil)
	handler := app.Handler()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("admin status = %d, want 401", rec.Code)
	}
	if rec.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("missing WWW-Authenticate header")
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/oidc/login", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("oidc login status = %d, want 404", rec.Code)
	}
}

func TestOIDCEnabledRedirectsAdminToLogin(t *testing.T) {
	_, _, _, handler := newOIDCApp(t, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin", nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("admin status = %d, want 303", rec.Code)
	}
	if location := rec.Header().Get("Location"); location != "/oidc/login" {
		t.Fatalf("location = %q, want /oidc/login", location)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/candidate", nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/oidc/login" {
		t.Fatalf("candidate POST status = %d location %q, want redirect to login", rec.Code, rec.Header().Get("Location"))
	}
}

func TestOIDCFullFlowRendersSessionAdminPage(t *testing.T) {
	_, store, idp, handler := newOIDCApp(t, nil)
	if err := store.UpsertCandidate(context.Background(), Candidate{Source: "test", URL: "https://example.test/event", Title: "Event"}); err != nil {
		t.Fatal(err)
	}
	sessionCookie := completeLogin(t, handler, idp)

	rec := httptest.NewRecorder()
	adminRequest := httptest.NewRequest(http.MethodGet, "/admin", nil)
	adminRequest.AddCookie(sessionCookie)
	handler.ServeHTTP(rec, adminRequest)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin status = %d, body %q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !csrfPattern.MatchString(body) {
		t.Fatal("admin page missing csrf hidden field")
	}
	if !strings.Contains(body, "Sign out") {
		t.Fatal("admin page missing sign out button")
	}
	if !strings.Contains(body, "Signed in as user@example.test") {
		t.Fatal("admin page missing signed-in identity")
	}
	if strings.Contains(body, "?token=") {
		t.Fatalf("session-mode page leaks token links: %s", body)
	}
}

func TestOIDCSessionModeRejectCandidateWithCSRF(t *testing.T) {
	_, store, idp, handler := newOIDCApp(t, nil)
	ctx := context.Background()
	candidate := Candidate{Source: "test", URL: "https://example.test/event", Title: "Event"}
	if err := store.UpsertCandidate(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	sessionCookie := completeLogin(t, handler, idp)

	rec := httptest.NewRecorder()
	adminRequest := httptest.NewRequest(http.MethodGet, "/admin", nil)
	adminRequest.AddCookie(sessionCookie)
	handler.ServeHTTP(rec, adminRequest)
	csrf := csrfPattern.FindStringSubmatch(rec.Body.String())[1]

	postForm := func(csrf string) *httptest.ResponseRecorder {
		form := url.Values{"url": {candidate.URL}, "action": {"reject"}}
		if csrf != "" {
			form.Set("csrf", csrf)
		}
		rec := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/admin/candidate", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.AddCookie(sessionCookie)
		handler.ServeHTTP(rec, request)
		return rec
	}

	rec = postForm(csrf)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("reject status = %d, body %q", rec.Code, rec.Body.String())
	}
	if location := rec.Header().Get("Location"); location != "/admin" {
		t.Fatalf("reject redirect = %q, want /admin", location)
	}
	stored, err := store.Candidate(ctx, candidate.URL)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != CandidateRejected {
		t.Fatalf("candidate status = %q, want rejected", stored.Status)
	}

	if rec := postForm(""); rec.Code != http.StatusForbidden {
		t.Fatalf("missing csrf status = %d, want 403", rec.Code)
	}
	if rec := postForm("wrong-token"); rec.Code != http.StatusForbidden {
		t.Fatalf("wrong csrf status = %d, want 403", rec.Code)
	}
}

func TestOIDCBreakGlassTokenStillWorks(t *testing.T) {
	_, store, idp, handler := newOIDCApp(t, nil)
	_ = idp
	if err := store.UpsertCandidate(context.Background(), Candidate{Source: "test", URL: "https://example.test/event", Title: "Event"}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin?token=admin-token", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("token-mode admin status = %d, body %q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "name=token") {
		t.Fatal("token-mode page missing hidden token fields")
	}
	if strings.Contains(body, "Sign out") || strings.Contains(body, "Signed in as") {
		t.Fatalf("token-mode page rendered session-mode elements: %s", body)
	}
	var legacyCookie bool
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == "radar_admin_session" {
			legacyCookie = true
		}
	}
	if !legacyCookie {
		t.Fatal("token-mode page did not set the legacy admin cookie")
	}

	rec = httptest.NewRecorder()
	bearerRequest := httptest.NewRequest(http.MethodGet, "/admin", nil)
	bearerRequest.Header.Set("Authorization", "Bearer admin-token")
	handler.ServeHTTP(rec, bearerRequest)
	if rec.Code != http.StatusOK {
		t.Fatalf("bearer admin status = %d", rec.Code)
	}
}

func TestOIDCLogoutFlow(t *testing.T) {
	_, store, idp, handler := newOIDCApp(t, nil)
	sessionCookie := completeLogin(t, handler, idp)

	rec := httptest.NewRecorder()
	logoutRequest := httptest.NewRequest(http.MethodPost, "/admin/logout", nil)
	logoutRequest.AddCookie(sessionCookie)
	handler.ServeHTTP(rec, logoutRequest)
	if rec.Code != http.StatusFound {
		t.Fatalf("logout status = %d, want 302", rec.Code)
	}
	if location := rec.Header().Get("Location"); location != "/admin" {
		t.Fatalf("logout location = %q, want /admin", location)
	}

	rec = httptest.NewRecorder()
	afterLogout := httptest.NewRequest(http.MethodGet, "/admin", nil)
	afterLogout.AddCookie(sessionCookie)
	handler.ServeHTTP(rec, afterLogout)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/oidc/login" {
		t.Fatalf("post-logout admin status = %d location %q, want redirect to login", rec.Code, rec.Header().Get("Location"))
	}

	var count int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM oidc_sessions`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("oidc_sessions rows = %d, want 0 after login+logout", count)
	}
}

func TestOIDCCalendarUntouched(t *testing.T) {
	_, _, idp, handler := newOIDCApp(t, nil)
	_ = idp
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/calendar/test-feed-token.ics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("calendar status = %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/calendar/wrong-token.ics", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("wrong calendar token status = %d, want 404", rec.Code)
	}
}

func TestOIDCPublicEndpointsStayUnauthenticated(t *testing.T) {
	_, _, idp, handler := newOIDCApp(t, nil)
	_ = idp
	for _, path := range []string{"/healthz", "/status"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", path, rec.Code)
		}
	}
}

func TestOIDCConfigValidationAndAuthConfigMapping(t *testing.T) {
	base := Config{
		Branding: Branding{Timezone: "UTC"},
		Runtime: Runtime{
			DatabasePath: "events.db", FeedToken: "secret", ListenAddress: "127.0.0.1:0",
			SyncInterval: time.Hour, HTTPTimeout: time.Second,
		},
		Sources: Sources{ICSFeeds: []ICSFeedConfig{{Name: "feed", URL: "https://example.test/events.ics"}}},
	}
	full := func(extra func(config *Config)) Config {
		config := base
		config.Runtime.OIDCIssuer = "https://idp.example"
		config.Runtime.OIDCClientID = "client"
		config.Runtime.OIDCClientSecret = "secret"
		config.Runtime.OIDCRedirectURL = "https://radar.example/oidc/callback"
		config.Runtime.OIDCAllowedEmails = []string{"user@example.org"}
		if extra != nil {
			extra(&config)
		}
		return config
	}
	for _, test := range []struct {
		name   string
		config Config
		want   string // required substring of the validation error, "" for ok
	}{
		{name: "no oidc vars", config: base},
		{name: "valid full set", config: full(nil)},
		{name: "http localhost issuer", config: full(func(c *Config) { c.Runtime.OIDCIssuer = "http://localhost:8080" })},
		{name: "http non-localhost issuer", config: full(func(c *Config) { c.Runtime.OIDCIssuer = "http://idp.example" }), want: "RADAR_OIDC_ISSUER"},
		{name: "issuer without allowlist", config: full(func(c *Config) { c.Runtime.OIDCAllowedEmails = nil }), want: "RADAR_OIDC_ALLOWED"},
		{name: "missing client id", config: full(func(c *Config) { c.Runtime.OIDCClientID = "" }), want: "RADAR_OIDC_CLIENT_ID"},
		{name: "missing client secret", config: full(func(c *Config) { c.Runtime.OIDCClientSecret = "" }), want: "RADAR_OIDC_CLIENT_SECRET"},
		{name: "missing redirect", config: full(func(c *Config) { c.Runtime.OIDCRedirectURL = "" }), want: "RADAR_OIDC_REDIRECT_URL"},
		{name: "redirect non-https", config: full(func(c *Config) { c.Runtime.OIDCRedirectURL = "https://radar.example/oidc/other" })},
		{name: "redirect insecure", config: full(func(c *Config) {
			c.Runtime.OIDCRedirectURL = "https://radar.example/oidc/cb"
			c.Runtime.OIDCRedirectURL = "http://radar.example/oidc/callback"
		}), want: "RADAR_OIDC_REDIRECT_URL"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.config.Validate()
			if test.want == "" {
				if err != nil {
					t.Fatalf("%s: unexpected error: %v", test.name, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("%s: validation error = %v, want substring %q", test.name, err, test.want)
			}
		})
	}

	config := full(func(c *Config) {
		c.Runtime.OIDCScopes = []string{"openid", "groups"}
		c.Runtime.OIDCAllowedDomains = []string{"Example.ORG"}
		c.Runtime.OIDCAllowedSubs = []string{"sub-a", "sub-b"}
		c.Runtime.TrustProxy = true
	})
	if !config.OIDCConfigured() {
		t.Fatal("OIDCConfigured() = false with issuer set")
	}
	if (base).OIDCConfigured() {
		t.Fatal("OIDCConfigured() = true without issuer")
	}
	authConfig := config.AuthConfig()
	if authConfig.Issuer != config.Runtime.OIDCIssuer || authConfig.ClientID != config.Runtime.OIDCClientID ||
		authConfig.ClientSecret != config.Runtime.OIDCClientSecret || authConfig.RedirectURL != config.Runtime.OIDCRedirectURL ||
		authConfig.TrustProxy != config.Runtime.TrustProxy {
		t.Fatalf("AuthConfig scalar mapping mismatch: %#v", authConfig)
	}
	if strings.Join(authConfig.Scopes, " ") != "openid groups" ||
		strings.Join(authConfig.AllowedEmails, ",") != "user@example.org" ||
		strings.Join(authConfig.AllowedDomains, ",") != "Example.ORG" ||
		strings.Join(authConfig.AllowedSubs, ",") != "sub-a,sub-b" {
		t.Fatalf("AuthConfig list mapping mismatch: %#v", authConfig)
	}
}
