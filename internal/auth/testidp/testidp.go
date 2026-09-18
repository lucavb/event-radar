// Package testidp provides a socket-free fake OpenID Connect provider for
// tests: discovery, JWKS, authorize, and token endpoints backed by an RSA key
// that signs ID tokens directly (no JWT library). It is test-only support code
// and not part of the application; importing it outside tests is a bug.
//
// The provider's HTTP handler is served in-process through a custom
// RoundTripper (no loopback TCP), so endpoints use a fixed https:// URL space
// identical to what an httptest.Server would serve. Requests flow through
// idp.Client() or a context prepared with idp.Context().
package testidp

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// IDP is a minimal OpenID Connect provider. The claim fields are knobs: tests
// set them before the token endpoint is hit to inject wrong nonce/audience or
// custom identities. An empty Nonce means "echo the nonce the flow requested".
type IDP struct {
	Base          string
	Sub           string
	Email         string
	Name          string
	Nonce         string
	Aud           string
	EmailVerified bool

	key            *rsa.PrivateKey
	kid            string
	mu             sync.Mutex
	authorizeQuery url.Values
	tokenForm      url.Values
}

// New creates a fake IdP with stable defaults: sub "sub-1", email
// "user@example.test", audience "radar-test", verified email.
func New(tb testing.TB) *IDP {
	tb.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		tb.Fatal(err)
	}
	return &IDP{
		Base:          "https://idp.test",
		Sub:           "sub-1",
		Email:         "user@example.test",
		Name:          "Test User",
		Aud:           "radar-test",
		EmailVerified: true,
		key:           key,
		kid:           "test-key-1",
	}
}

// transport serves requests against the fake IdP handler without sockets.
type transport struct{ handler http.Handler }

func (tr transport) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	tr.handler.ServeHTTP(rec, req.Clone(context.Background()))
	return rec.Result(), nil
}

// Client returns an http.Client that serves the fake IdP in-process and does
// not follow redirects, so callers can inspect each hop.
func (idp *IDP) Client() *http.Client {
	return &http.Client{
		Transport:     transport{handler: idp.Handler()},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Context returns ctx extended with the fake IdP's client as the HTTP client
// used by golang.org/x/oauth2 and github.com/coreos/go-oidc/v3.
func (idp *IDP) Context(ctx context.Context) context.Context {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, idp.Client())
	return oidc.ClientContext(ctx, idp.Client())
}

// AuthorizeQuery returns the query parameters of the last /authorize request.
func (idp *IDP) AuthorizeQuery() url.Values {
	idp.mu.Lock()
	defer idp.mu.Unlock()
	return idp.authorizeQuery
}

// TokenForm returns the form values of the last /token request.
func (idp *IDP) TokenForm() url.Values {
	idp.mu.Lock()
	defer idp.mu.Unlock()
	return idp.tokenForm
}

func (idp *IDP) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                idp.Base,
			"authorization_endpoint":                idp.Base + "/authorize",
			"token_endpoint":                        idp.Base + "/token",
			"jwks_uri":                              idp.Base + "/jwks.json",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("GET /jwks.json", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "kid": idp.kid, "alg": "RS256", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(idp.key.N.Bytes()),
			"e": "AQAB",
		}}})
	})
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		idp.mu.Lock()
		idp.authorizeQuery = r.URL.Query()
		idp.mu.Unlock()
		redirect := r.URL.Query().Get("redirect_uri")
		if redirect == "" {
			http.Error(w, "missing redirect_uri", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, redirect+"?code=fake-code&state="+url.QueryEscape(r.URL.Query().Get("state")), http.StatusFound)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		idp.mu.Lock()
		idp.tokenForm = r.PostForm
		nonce := idp.Nonce
		if nonce == "" {
			// Default: echo back the nonce the flow requested.
			nonce = idp.authorizeQuery.Get("nonce")
		}
		idp.mu.Unlock()
		now := time.Now().Unix()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at",
			"token_type":   "Bearer",
			"id_token": idp.SignIDToken(map[string]any{
				"iss":            idp.Base,
				"sub":            idp.Sub,
				"aud":            idp.Aud,
				"iat":            now,
				"exp":            now + 3600,
				"nonce":          nonce,
				"email":          idp.Email,
				"email_verified": idp.EmailVerified,
				"name":           idp.Name,
			}),
		})
	})
	return mux
}

// SignIDToken signs the claims with RS256 (SHA-256) and the fake IdP key.
func (idp *IDP) SignIDToken(claims map[string]any) string {
	header, err := json.Marshal(map[string]any{"alg": "RS256", "kid": idp.kid})
	if err != nil {
		panic(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		panic(err)
	}
	segment := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(segment))
	signature, err := rsa.SignPKCS1v15(rand.Reader, idp.key, crypto.SHA256, digest[:])
	if err != nil {
		panic(err)
	}
	return segment + "." + base64.RawURLEncoding.EncodeToString(signature)
}
