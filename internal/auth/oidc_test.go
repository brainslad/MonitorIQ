package auth

import (
	"context"
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

	jose "github.com/go-jose/go-jose/v4"
)

// fakeIdP is a minimal OpenID Connect provider that approves every
// authorization request for the configured claims.
type fakeIdP struct {
	*httptest.Server
	key    *rsa.PrivateKey
	claims map[string]any

	mu    sync.Mutex
	codes map[string]map[string]string // code -> nonce, challenge, redirect
}

func newFakeIdP(t *testing.T, claims map[string]any) *fakeIdP {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIdP{key: key, claims: claims, codes: map[string]map[string]string{}}
	mux := http.NewServeMux()
	f.Server = httptest.NewServer(mux)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": f.URL, "authorization_endpoint": f.URL + "/authorize", "token_endpoint": f.URL + "/token",
			"jwks_uri": f.URL + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("client_id") != "miq" {
			http.Error(w, "bad request", 400)
			return
		}
		code := randomString(8)
		f.mu.Lock()
		f.codes[code] = map[string]string{"nonce": q.Get("nonce"), "challenge": q.Get("code_challenge"), "redirect": q.Get("redirect_uri")}
		f.mu.Unlock()
		to, _ := url.Parse(q.Get("redirect_uri"))
		v := to.Query()
		v.Set("code", code)
		v.Set("state", q.Get("state"))
		to.RawQuery = v.Encode()
		http.Redirect(w, r, to.String(), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		id, secret, _ := r.BasicAuth()
		if id == "" {
			id, secret = r.PostFormValue("client_id"), r.PostFormValue("client_secret")
		}
		f.mu.Lock()
		c, ok := f.codes[r.PostFormValue("code")]
		delete(f.codes, r.PostFormValue("code"))
		f.mu.Unlock()
		sum := sha256.Sum256([]byte(r.PostFormValue("code_verifier")))
		if !ok || id != "miq" || secret != "s3cret" || c["redirect"] != r.PostFormValue("redirect_uri") ||
			base64.RawURLEncoding.EncodeToString(sum[:]) != c["challenge"] {
			w.WriteHeader(400)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		claims := map[string]any{"iss": f.URL, "aud": "miq", "sub": "user-123", "nonce": c["nonce"],
			"iat": time.Now().Unix(), "exp": time.Now().Add(5 * time.Minute).Unix()}
		for k, v := range f.claims {
			claims[k] = v
		}
		signer, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: f.key, KeyID: "k1"}}, nil)
		payload, _ := json.Marshal(claims)
		jws, _ := signer.Sign(payload)
		tok, _ := jws.CompactSerialize()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 300, "id_token": tok})
	})
	return f
}

func TestOIDCFlow(t *testing.T) {
	idp := newFakeIdP(t, map[string]any{"preferred_username": "jane@example.com", "name": "Jane Doe", "groups": []string{"ops"}})
	defer idp.Close()
	cfg := &OIDCConfig{Enabled: true, Issuer: idp.URL, ClientID: "miq", ClientSecret: "s3cret", Scopes: "openid profile email",
		UsernameClaim: "preferred_username", GroupsClaim: "groups", RequiredGroups: "ops", AllowedDomains: "example.com"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	o := &OIDC{}
	ctx := context.Background()
	const redirect = "https://status.example.com/auth/oidc/callback"
	login := func() (*Flow, string, string) {
		to, flow, err := o.AuthURL(ctx, cfg, redirect, "/monitors/1")
		if err != nil {
			t.Fatal(err)
		}
		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := client.Get(to)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		back, _ := url.Parse(resp.Header.Get("Location"))
		return flow, back.Query().Get("code"), back.Query().Get("state")
	}

	flow, code, state := login()
	if state != flow.State {
		t.Fatal("state not returned")
	}
	id, err := o.Exchange(ctx, cfg, redirect, flow, code)
	if err != nil {
		t.Fatal(err)
	}
	if id.Username != "jane@example.com" || id.DisplayName != "Jane Doe" || id.ExternalID != idp.URL+"|user-123" {
		t.Fatalf("%+v", id)
	}
	// A code can be redeemed once.
	if _, err := o.Exchange(ctx, cfg, redirect, flow, code); err == nil {
		t.Fatal("code reuse accepted")
	}
	// A wrong nonce (token from another login) is rejected.
	flow2, code2, _ := login()
	flow2.Nonce = "other"
	if _, err := o.Exchange(ctx, cfg, redirect, flow2, code2); err == nil {
		t.Fatal("nonce mismatch accepted")
	}
	// A wrong PKCE verifier is rejected.
	flow3, code3, _ := login()
	flow3.Verifier = "x" + flow3.Verifier[1:]
	if _, err := o.Exchange(ctx, cfg, redirect, flow3, code3); err == nil {
		t.Fatal("PKCE not enforced")
	}
	// Group restriction.
	cfg.RequiredGroups = "admins"
	flow4, code4, _ := login()
	if _, err := o.Exchange(ctx, cfg, redirect, flow4, code4); err != ErrDenied {
		t.Fatal("group restriction not enforced", err)
	}
	// Expired flow.
	cfg.RequiredGroups = ""
	flow5, code5, _ := login()
	flow5.Expires = time.Now().Add(-time.Second).Unix()
	if _, err := o.Exchange(ctx, cfg, redirect, flow5, code5); err == nil {
		t.Fatal("expired flow accepted")
	}
}
