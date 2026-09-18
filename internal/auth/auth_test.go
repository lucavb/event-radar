package auth

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lucabecker/event-radar/internal/auth/testidp"

	_ "modernc.org/sqlite"
)

func newTestAuth(t *testing.T, cfg Config) (*Auth, *testidp.IDP, *sql.DB) {
	t.Helper()
	idp := testidp.New(t)
	cfg.Issuer = idp.Base
	if cfg.ClientID == "" {
		cfg.ClientID = "radar-test"
	}
	if cfg.ClientSecret == "" {
		cfg.ClientSecret = "radar-secret"
	}
	if cfg.RedirectURL == "" {
		cfg.RedirectURL = "http://127.0.0.1:1/oidc/callback"
	}
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	a, err := New(idp.Context(context.Background()), cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	return a, idp, db
}

// login runs the login handler and returns the response and the authorize URL.
func login(t *testing.T, a *Auth, returnTo string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	target := "/oidc/login"
	if returnTo != "" {
		target += "?return_to=" + url.QueryEscape(returnTo)
	}
	rec := httptest.NewRecorder()
	a.LoginHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("login status = %d, want 302", rec.Code)
	}
	return rec, rec.Header().Get("Location")
}

func stateFromAuthorizeURL(t *testing.T, location string) url.Values {
	t.Helper()
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Query()
}

// callbackFromLogin runs the callback with the code/state from the login's
// authorize URL. Tests that skip the /authorize hop must still get the flow's
// nonce echoed into the signed ID token, so copy it into the fake IdP.
func callbackFromLogin(t *testing.T, a *Auth, idp *testidp.IDP, cookie *http.Cookie, location string) *httptest.ResponseRecorder {
	t.Helper()
	query := stateFromAuthorizeURL(t, location)
	if idp != nil && idp.Nonce == "" {
		idp.Nonce = query.Get("nonce")
	}
	return callback(t, a, cookie, "code=fake-code&state="+query.Get("state"))
}

func callback(t *testing.T, a *Auth, cookie *http.Cookie, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/oidc/callback?"+query, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	a.CallbackHandler().ServeHTTP(rec, req)
	return rec
}

func cookieNamed(t *testing.T, rec *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("no %q cookie in response", name)
	return nil
}

func requestWithCookie(t *testing.T, a *Auth, cookie *http.Cookie, path string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(cookie)
	return req
}

func TestLoginRedirectsToAuthorizeWithStateNonceChallengeAndCookie(t *testing.T) {
	a, idp, _ := newTestAuth(t, Config{AllowedEmails: []string{"user@example.test"}})
	rec, location := login(t, a, "")
	if !strings.HasPrefix(location, idp.Base+"/authorize") {
		t.Fatalf("location = %q, want authorize URL", location)
	}
	query := stateFromAuthorizeURL(t, location)
	if query.Get("client_id") != "radar-test" {
		t.Fatalf("client_id = %q", query.Get("client_id"))
	}
	if query.Get("response_type") != "code" {
		t.Fatalf("response_type = %q", query.Get("response_type"))
	}
	if query.Get("nonce") == "" {
		t.Fatal("authorize URL has no nonce")
	}
	if query.Get("code_challenge") == "" || query.Get("code_challenge_method") != "S256" {
		t.Fatalf("PKCE missing: challenge=%q method=%q", query.Get("code_challenge"), query.Get("code_challenge_method"))
	}
	scopes := strings.Fields(query.Get("scope"))
	for _, want := range []string{"openid", "profile", "email"} {
		found := false
		for _, scope := range scopes {
			if scope == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("scope %q missing from %q", want, query.Get("scope"))
		}
	}
	cookie := cookieNamed(t, rec, sessionCookieName)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie attributes = %#v", cookie)
	}
	if cookie.Value == "" {
		t.Fatal("cookie value empty")
	}
}

func TestFullLoginFlowAuthenticatesAndRotatesSession(t *testing.T) {
	a, idp, _ := newTestAuth(t, Config{AllowedEmails: []string{"user@example.test"}})
	rec, location := login(t, a, "")
	oldCookie := cookieNamed(t, rec, sessionCookieName)

	authorizeResponse, err := idp.Client().Get(location)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = authorizeResponse.Body.Close() }()
	if authorizeResponse.StatusCode != http.StatusFound {
		t.Fatalf("authorize status = %d", authorizeResponse.StatusCode)
	}
	authorizeTarget, err := url.Parse(authorizeResponse.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if authorizeTarget.Query().Get("code") != "fake-code" {
		t.Fatalf("authorize response = %q", authorizeResponse.Header.Get("Location"))
	}
	query := stateFromAuthorizeURL(t, location)
	if got := idp.AuthorizeQuery().Get("nonce"); got != query.Get("nonce") {
		t.Fatalf("authorize nonce = %q, want %q", got, query.Get("nonce"))
	}
	if idp.AuthorizeQuery().Get("redirect_uri") != "http://127.0.0.1:1/oidc/callback" {
		t.Fatalf("redirect_uri = %q", idp.AuthorizeQuery().Get("redirect_uri"))
	}

	rec = callback(t, a, oldCookie, authorizeTarget.Query().Encode())
	if rec.Code != http.StatusFound {
		t.Fatalf("callback status = %d, body %q", rec.Code, rec.Body.String())
	}
	if location := rec.Header().Get("Location"); location != "/admin" {
		t.Fatalf("callback location = %q, want /admin", location)
	}
	newCookie := cookieNamed(t, rec, sessionCookieName)
	if newCookie.Value == oldCookie.Value {
		t.Fatal("session cookie was not rotated")
	}
	session := a.Session(requestWithCookie(t, a, newCookie, "/admin"))
	if session == nil {
		t.Fatal("Session() = nil after successful flow")
	}
	if session.UserSub != idp.Sub || session.Email != idp.Email || session.Name != idp.Name {
		t.Fatalf("session = %#v", session)
	}
	if session.CSRFToken == "" {
		t.Fatal("session CSRFToken empty")
	}
	if idp.TokenForm().Get("code") != "fake-code" || idp.TokenForm().Get("code_verifier") == "" {
		t.Fatalf("token form = %#v", idp.TokenForm())
	}
}

func TestCallbackRejectsStateMismatch(t *testing.T) {
	a, _, _ := newTestAuth(t, Config{AllowedEmails: []string{"user@example.test"}})
	rec, _ := login(t, a, "")
	cookie := cookieNamed(t, rec, sessionCookieName)
	rec = callback(t, a, cookie, "code=fake-code&state=wrong-state")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("callback status = %d, want 400", rec.Code)
	}
}

func TestCallbackRejectsMissingState(t *testing.T) {
	a, _, _ := newTestAuth(t, Config{AllowedEmails: []string{"user@example.test"}})
	rec, _ := login(t, a, "")
	cookie := cookieNamed(t, rec, sessionCookieName)
	rec = callback(t, a, cookie, "code=fake-code")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("callback status = %d, want 400", rec.Code)
	}
	if cleared := cookieNamed(t, rec, sessionCookieName); cleared.MaxAge != -1 {
		t.Fatalf("clear cookie MaxAge = %d, want -1", cleared.MaxAge)
	}
	// The pre-auth row is not deleted; it expires via Sweep.
	if _, err := a.store.Get(context.Background(), cookie.Value); err != nil {
		t.Fatalf("pre-auth row deleted on missing state: %v", err)
	}
}

func TestCallbackRejectsWrongState(t *testing.T) {
	a, _, _ := newTestAuth(t, Config{AllowedEmails: []string{"user@example.test"}})
	rec, _ := login(t, a, "")
	cookie := cookieNamed(t, rec, sessionCookieName)
	rec = callback(t, a, cookie, "code=fake-code&state=wrong-state")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("callback status = %d, want 400", rec.Code)
	}
	if cleared := cookieNamed(t, rec, sessionCookieName); cleared.MaxAge != -1 {
		t.Fatalf("clear cookie MaxAge = %d, want -1", cleared.MaxAge)
	}
	if _, err := a.store.Get(context.Background(), cookie.Value); err != nil {
		t.Fatalf("pre-auth row deleted on wrong state: %v", err)
	}
}

// callbackWithSessionOnly runs the callback with only the session cookie and
// extra raw query (no state), for the authenticated-row regression tests.
func callbackWithSessionOnly(t *testing.T, a *Auth, cookie *http.Cookie, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/oidc/callback?"+query, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	a.CallbackHandler().ServeHTTP(rec, req)
	return rec
}

func assertAuthenticatedRowIntact(t *testing.T, a *Auth, cookie *http.Cookie) {
	t.Helper()
	if _, err := a.store.Get(context.Background(), cookie.Value); err != nil {
		t.Fatalf("authenticated row was deleted: %v", err)
	}
	if a.Session(requestWithCookie(t, a, cookie, "/admin")) == nil {
		t.Fatal("session invalidated by callback")
	}
}

func assertSessionCookieNotCleared(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == sessionCookieName && cookie.MaxAge < 0 {
			t.Fatal("session cookie was cleared")
		}
	}
}

func TestCallbackRejectsAuthenticatedRowWithoutState(t *testing.T) {
	a, idp, _ := newTestAuth(t, Config{AllowedEmails: []string{"user@example.test"}})
	rec, _ := login(t, a, "")
	cookie := cookieNamed(t, rec, sessionCookieName)
	rec = callbackFromLogin(t, a, idp, cookie, rec.Header().Get("Location"))
	if rec.Code != http.StatusFound {
		t.Fatalf("login flow status = %d, want 302", rec.Code)
	}
	sessionCookie := cookieNamed(t, rec, sessionCookieName)

	rec = callbackWithSessionOnly(t, a, sessionCookie, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("callback status = %d, want 400", rec.Code)
	}
	assertSessionCookieNotCleared(t, rec)
	assertAuthenticatedRowIntact(t, a, sessionCookie)
}

func TestCallbackRejectsAuthenticatedRowWithErrorParam(t *testing.T) {
	a, idp, _ := newTestAuth(t, Config{AllowedEmails: []string{"user@example.test"}})
	rec, _ := login(t, a, "")
	cookie := cookieNamed(t, rec, sessionCookieName)
	rec = callbackFromLogin(t, a, idp, cookie, rec.Header().Get("Location"))
	if rec.Code != http.StatusFound {
		t.Fatalf("login flow status = %d, want 302", rec.Code)
	}
	sessionCookie := cookieNamed(t, rec, sessionCookieName)

	rec = callbackWithSessionOnly(t, a, sessionCookie, "error=access_denied")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("callback status = %d, want 400", rec.Code)
	}
	assertSessionCookieNotCleared(t, rec)
	assertAuthenticatedRowIntact(t, a, sessionCookie)
}

func TestCallbackRejectsNonceMismatch(t *testing.T) {
	a, idp, _ := newTestAuth(t, Config{AllowedEmails: []string{"user@example.test"}})
	rec, _ := login(t, a, "")
	cookie := cookieNamed(t, rec, sessionCookieName)
	idp.Nonce = "wrong-nonce"
	rec = callbackFromLogin(t, a, idp, cookie, rec.Header().Get("Location"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("callback status = %d, want 401", rec.Code)
	}
}

func TestCallbackRejectsWrongAudience(t *testing.T) {
	a, idp, _ := newTestAuth(t, Config{AllowedEmails: []string{"user@example.test"}})
	rec, _ := login(t, a, "")
	cookie := cookieNamed(t, rec, sessionCookieName)
	idp.Aud = "other-client"
	rec = callbackFromLogin(t, a, idp, cookie, rec.Header().Get("Location"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("callback status = %d, want 401", rec.Code)
	}
}

func TestCallbackRejectsExpiredPreAuthSessionAndClearsCookie(t *testing.T) {
	a, _, db := newTestAuth(t, Config{AllowedEmails: []string{"user@example.test"}})
	rec, location := login(t, a, "")
	cookie := cookieNamed(t, rec, sessionCookieName)
	if _, err := db.Exec(`UPDATE oidc_sessions SET expires_at = ?`, time.Now().Add(-time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	rec = callbackFromLogin(t, a, nil, cookie, location)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("callback status = %d, want 400", rec.Code)
	}
	if cleared := cookieNamed(t, rec, sessionCookieName); cleared.MaxAge != -1 {
		t.Fatalf("clear cookie MaxAge = %d, want -1", cleared.MaxAge)
	}
}

func TestCallbackIsSingleUse(t *testing.T) {
	a, idp, _ := newTestAuth(t, Config{AllowedEmails: []string{"user@example.test"}})
	rec, location := login(t, a, "")
	oldCookie := cookieNamed(t, rec, sessionCookieName)
	rec = callbackFromLogin(t, a, idp, oldCookie, location)
	if rec.Code != http.StatusFound {
		t.Fatalf("first callback status = %d, body %q", rec.Code, rec.Body.String())
	}
	newCookie := cookieNamed(t, rec, sessionCookieName)
	rec = callbackFromLogin(t, a, nil, oldCookie, location)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("replayed callback status = %d, want 400", rec.Code)
	}
	if session := a.Session(requestWithCookie(t, a, oldCookie, "/admin")); session != nil {
		t.Fatal("old cookie still authenticates")
	}
	if session := a.Session(requestWithCookie(t, a, newCookie, "/admin")); session == nil {
		t.Fatal("new cookie lost after replay attempt")
	}
}

func TestAllowlistDecisions(t *testing.T) {
	for _, test := range []struct {
		name     string
		cfg      Config
		sub      string
		email    string
		verified bool
		want     int
	}{
		{name: "unlisted email", cfg: Config{AllowedEmails: []string{"alice@example.test"}}, email: "bob@example.test", verified: true, want: http.StatusForbidden},
		{name: "unverified email", cfg: Config{AllowedEmails: []string{"user@example.test"}}, email: "user@example.test", verified: false, want: http.StatusForbidden},
		{name: "sub allowlisted with unlisted email", cfg: Config{AllowedSubs: []string{"sub-1"}, AllowedEmails: []string{"alice@example.test"}}, sub: "sub-1", email: "bob@unlisted.test", verified: false, want: http.StatusFound},
		{name: "domain allowlisted", cfg: Config{AllowedDomains: []string{"example.test"}}, email: "anyone@example.test", verified: true, want: http.StatusFound},
		{name: "empty allowlist denies", cfg: Config{}, email: "user@example.test", verified: true, want: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, idp, _ := newTestAuth(t, test.cfg)
			rec, _ := login(t, a, "")
			cookie := cookieNamed(t, rec, sessionCookieName)
			idp.Sub, idp.Email, idp.EmailVerified = test.sub, test.email, test.verified
			rec = callbackFromLogin(t, a, idp, cookie, rec.Header().Get("Location"))
			if rec.Code != test.want {
				t.Fatalf("callback status = %d, want %d", rec.Code, test.want)
			}
		})
	}
}

func TestSessionNilCases(t *testing.T) {
	ctx := context.Background()
	a, _, db := newTestAuth(t, Config{AllowedEmails: []string{"user@example.test"}})
	expired := sessionRow{
		ID: rand.Text(), CSRFToken: rand.Text(), UserSub: "sub-1", Email: "user@example.test", Name: "User",
		CreatedAt: time.Now().Add(-9 * time.Hour), ExpiresAt: time.Now().Add(-time.Hour),
	}
	if err := a.store.Create(ctx, expired); err != nil {
		t.Fatal(err)
	}
	preAuth := sessionRow{
		ID: rand.Text(), State: "s", Nonce: "n", CodeVerifier: "v", ReturnTo: "/admin", CSRFToken: rand.Text(),
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(10 * time.Minute),
	}
	if err := a.store.Create(ctx, preAuth); err != nil {
		t.Fatal(err)
	}
	if session := a.Session(httptest.NewRequest(http.MethodGet, "/admin", nil)); session != nil {
		t.Fatal("Session() non-nil without cookie")
	}
	if session := a.Session(requestWithCookie(t, a, &http.Cookie{Name: sessionCookieName, Value: expired.ID}, "/admin")); session != nil {
		t.Fatal("Session() non-nil for expired authenticated row")
	}
	if session := a.Session(requestWithCookie(t, a, &http.Cookie{Name: sessionCookieName, Value: preAuth.ID}, "/admin")); session != nil {
		t.Fatal("Session() non-nil for pre-auth-only row")
	}
	if session := a.Session(requestWithCookie(t, a, &http.Cookie{Name: sessionCookieName, Value: "unknown"}, "/admin")); session != nil {
		t.Fatal("Session() non-nil for unknown cookie")
	}
	_ = db
}

func TestLogoutDeletesRowAndClearsCookie(t *testing.T) {
	a, idp, _ := newTestAuth(t, Config{AllowedEmails: []string{"user@example.test"}})
	rec, _ := login(t, a, "")
	cookie := cookieNamed(t, rec, sessionCookieName)
	rec = callbackFromLogin(t, a, idp, cookie, rec.Header().Get("Location"))
	if rec.Code != http.StatusFound {
		t.Fatalf("callback status = %d", rec.Code)
	}
	sessionCookie := cookieNamed(t, rec, sessionCookieName)

	logoutReq := httptest.NewRequest(http.MethodPost, "/admin/logout", nil)
	logoutReq.AddCookie(sessionCookie)
	logoutRec := httptest.NewRecorder()
	a.LogoutHandler().ServeHTTP(logoutRec, logoutReq)
	if logoutRec.Code != http.StatusFound {
		t.Fatalf("logout status = %d, want 302", logoutRec.Code)
	}
	if location := logoutRec.Header().Get("Location"); location != "/admin" {
		t.Fatalf("logout location = %q, want /admin", location)
	}
	if cleared := cookieNamed(t, logoutRec, sessionCookieName); cleared.MaxAge != -1 {
		t.Fatalf("logout clear cookie MaxAge = %d, want -1", cleared.MaxAge)
	}
	if session := a.Session(requestWithCookie(t, a, sessionCookie, "/admin")); session != nil {
		t.Fatal("session survived logout")
	}

	// Logout without a valid session is still a clean redirect.
	bareRec := httptest.NewRecorder()
	a.LogoutHandler().ServeHTTP(bareRec, httptest.NewRequest(http.MethodPost, "/admin/logout", nil))
	if bareRec.Code != http.StatusFound {
		t.Fatalf("bare logout status = %d, want 302", bareRec.Code)
	}

	// Non-POST is rejected.
	getRec := httptest.NewRecorder()
	a.LogoutHandler().ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, "/admin/logout", nil))
	if getRec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET logout status = %d, want 405", getRec.Code)
	}
}

func TestCookieSecureLogic(t *testing.T) {
	for _, test := range []struct {
		name       string
		trustProxy bool
		tls        bool
		proxyProto string
		wantSecure bool
	}{
		{name: "no tls no proxy", trustProxy: false, wantSecure: false},
		{name: "trusted proxy https", trustProxy: true, proxyProto: "https", wantSecure: true},
		{name: "trusted proxy http", trustProxy: true, proxyProto: "http", wantSecure: false},
		{name: "untrusted proxy header", trustProxy: false, proxyProto: "https", wantSecure: false},
		{name: "direct tls", trustProxy: false, tls: true, wantSecure: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, _, _ := newTestAuth(t, Config{TrustProxy: test.trustProxy, AllowedEmails: []string{"user@example.test"}})
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/oidc/login", nil)
			if test.tls {
				req.TLS = &tls.ConnectionState{}
			}
			if test.proxyProto != "" {
				req.Header.Set("X-Forwarded-Proto", test.proxyProto)
			}
			a.LoginHandler().ServeHTTP(rec, req)
			if rec.Code != http.StatusFound {
				t.Fatalf("login status = %d", rec.Code)
			}
			cookie := cookieNamed(t, rec, sessionCookieName)
			if cookie.Secure != test.wantSecure {
				t.Fatalf("cookie Secure = %v, want %v", cookie.Secure, test.wantSecure)
			}
		})
	}
}

func TestReturnToSanitization(t *testing.T) {
	for _, test := range []struct{ name, returnTo, requestTarget string }{
		{name: "absolute url", returnTo: "https://evil.example.test"},
		{name: "protocol relative", returnTo: "//evil.example.test"},
		{name: "backslash path", returnTo: "/\\evil.example.test"},
		// The browser sends the backslash percent-encoded; Query().Get()
		// decodes it, so this exercises the same rejection as "backslash path".
		{name: "query encoded backslash", requestTarget: "/oidc/login?return_to=/%5Cevil.example.test"},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, idp, _ := newTestAuth(t, Config{AllowedEmails: []string{"user@example.test"}})
			var rec *httptest.ResponseRecorder
			var location string
			if test.requestTarget != "" {
				rec = httptest.NewRecorder()
				a.LoginHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, test.requestTarget, nil))
				if rec.Code != http.StatusFound {
					t.Fatalf("login status = %d, want 302", rec.Code)
				}
				location = rec.Header().Get("Location")
			} else {
				rec, location = login(t, a, test.returnTo)
			}
			if !strings.HasPrefix(location, "/oidc/") && !strings.HasPrefix(location, "http") {
				t.Fatalf("location = %q", location)
			}
			cookie := cookieNamed(t, rec, sessionCookieName)
			rec = callbackFromLogin(t, a, idp, cookie, location)
			if rec.Code != http.StatusFound {
				t.Fatalf("callback status = %d, body %q", rec.Code, rec.Body.String())
			}
			if location := rec.Header().Get("Location"); location != "/admin" || strings.Contains(location, "evil") {
				t.Fatalf("callback location = %q, want /admin", location)
			}
		})
	}
}

func TestCallbackHandlesIdPError(t *testing.T) {
	a, _, _ := newTestAuth(t, Config{AllowedEmails: []string{"user@example.test"}})
	rec, _ := login(t, a, "")
	cookie := cookieNamed(t, rec, sessionCookieName)
	// The error branch runs only for pre-auth rows with a valid state, so the
	// IdP error is carried together with the flow's state.
	state := stateFromAuthorizeURL(t, rec.Header().Get("Location")).Get("state")
	rec = callback(t, a, cookie, "code=fake-code&state="+state+"&error=access_denied&error_description=internal+idp+detail+xyz")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("callback status = %d, want 401", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "authorization failed") {
		t.Fatalf("body = %q, want generic message", body)
	}
	if strings.Contains(body, "internal idp detail xyz") {
		t.Fatalf("body leaked error_description: %q", body)
	}
}

func TestNilAuthSafety(t *testing.T) {
	var a *Auth
	if a.Enabled() {
		t.Fatal("nil auth Enabled() = true")
	}
	if session := a.Session(httptest.NewRequest(http.MethodGet, "/admin", nil)); session != nil {
		t.Fatal("nil auth Session() non-nil")
	}
}
