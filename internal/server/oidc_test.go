package server

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-movement/internal/config"
	"github.com/abjelosevic88/pocket-homelab-movement/internal/store"
)

// fakeIdP is a tiny OpenID provider: it signs whoever `email` says.
type fakeIdP struct {
	*httptest.Server
	key, forger *rsa.PrivateKey
	clientID    string
	secret      string
	email       string
	verified    bool
	forge       bool
	nonces      map[string]string // code → nonce
	challenges  map[string]string // code → PKCE challenge
}

func newFakeIdP(t *testing.T, clientID, secret string) *fakeIdP {
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	f, _ := rsa.GenerateKey(rand.Reader, 2048)
	p := &fakeIdP{key: k, forger: f, clientID: clientID, secret: secret, verified: true, nonces: map[string]string{}, challenges: map[string]string{}}
	mux := http.NewServeMux()
	p.Server = httptest.NewServer(mux)
	b64 := base64.RawURLEncoding.EncodeToString
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"issuer": p.URL, "authorization_endpoint": p.URL + "/authorize", "token_endpoint": p.URL + "/token", "jwks_uri": p.URL + "/jwks"})
	})
	mux.HandleFunc("/api/v1/auth/subject", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"active": true, "iss": p.URL})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "use": "sig", "kid": "k1", "n": b64(k.N.Bytes()), "e": b64(big.NewInt(int64(k.E)).Bytes())}}})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("client_id") != p.clientID || q.Get("code_challenge_method") != "S256" {
			http.Error(w, "bad request", 400)
			return
		}
		code := store.RandomToken(8)
		p.nonces[code], p.challenges[code] = q.Get("nonce"), q.Get("code_challenge")
		http.Redirect(w, r, q.Get("redirect_uri")+"?"+url.Values{"code": {code}, "state": {q.Get("state")}}.Encode(), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		id, secret, _ := r.BasicAuth()
		code := r.FormValue("code")
		sum := sha256.Sum256([]byte(r.FormValue("code_verifier")))
		if id != p.clientID || secret != p.secret || p.challenges[code] != b64(sum[:]) {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		head, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"})
		body, _ := json.Marshal(map[string]any{"iss": p.URL, "sub": strings.ToLower(p.email), "auth_revision": int64(1), "aud": p.clientID, "exp": time.Now().Add(time.Hour).Unix(), "nonce": p.nonces[code], "email": p.email, "email_verified": p.verified, "name": "Aleksandar"})
		signing := b64(head) + "." + b64(body)
		h := sha256.Sum256([]byte(signing))
		signer := p.key
		if p.forge {
			signer = p.forger
		}
		sig, _ := rsa.SignPKCS1v15(rand.Reader, signer, crypto.SHA256, h[:])
		json.NewEncoder(w).Encode(map[string]string{"id_token": signing + "." + b64(sig), "access_token": "at", "token_type": "Bearer"})
	})
	t.Cleanup(p.Close)
	return p
}

// oidcHarness starts the app in front of a fake provider and returns the
// browser dance plus a probe for /auth/me.
type oidcHarness struct {
	ts     *httptest.Server
	st     *store.Store
	signIn func(next string) (*url.URL, *http.Client)
	me     func(cl *http.Client) int
	status func() map[string]any
}

func newOIDCHarness(t *testing.T, cfg *config.Config) *oidcHarness {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(cfg.DBPath, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ts := httptest.NewUnstartedServer(nil)
	cfg.BaseURL = "http://" + ts.Listener.Addr().String()
	s := New(cfg, st, log, nil)
	if err := s.core.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	ts.Config.Handler = s.Router()
	ts.Start()
	t.Cleanup(ts.Close)
	h := &oidcHarness{ts: ts, st: st}
	h.signIn = func(next string) (*url.URL, *http.Client) {
		jar, _ := cookiejar.New(nil)
		var last *url.URL
		cl := &http.Client{Jar: jar, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			last = req.URL
			if strings.HasPrefix(req.URL.String(), ts.URL) && !strings.HasPrefix(req.URL.Path, "/api/") {
				return http.ErrUseLastResponse // back in the app
			}
			return nil
		}}
		res, err := cl.Get(ts.URL + "/api/v1/auth/oidc/start?next=" + url.QueryEscape(next))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return last, cl
	}
	h.me = func(cl *http.Client) int {
		res, err := cl.Get(ts.URL + "/api/v1/auth/me")
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	h.status = func() map[string]any {
		var status map[string]any
		res, err := http.Get(ts.URL + "/api/v1/auth/status")
		if err != nil {
			t.Fatal(err)
		}
		json.NewDecoder(res.Body).Decode(&status)
		res.Body.Close()
		return status
	}
	return h
}

func TestOIDCSignIn(t *testing.T) {
	idp := newFakeIdP(t, "movement", "shh")
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir, DBPath: dir + "/t.db", AuthMode: "local", Geocoder: "off",
		SessionTTL: time.Hour, OIDCIssuer: idp.URL, OIDCClientID: "movement", OIDCClientSecret: "shh", OIDCName: "PocketID", OIDCAutoCreate: false}
	h := newOIDCHarness(t, cfg)
	ctx := context.Background()
	_ = h.st.CreateUser(ctx, &store.User{Email: "aleksandar@example.com", Name: "A", Role: "admin", PasswordHash: "x"})

	idp.email = "Aleksandar@Example.com"
	end, cl := h.signIn("/places")
	if end == nil || end.Path != "/places" || h.me(cl) != 200 {
		t.Fatalf("happy path ended at %v, me=%d", end, h.me(cl))
	}

	if status := h.status(); status["oidc"] != true || status["oidc_name"] != "PocketID" {
		t.Errorf("status %v", status)
	}

	for _, c := range []struct {
		name, email string
		verified    bool
		forge       bool
		next, want  string
	}{
		{"unknown email", "stranger@example.com", true, false, "/", "No Pocket Movement account"},
		{"unverified email", "aleksandar@example.com", false, false, "/", "verified email"},
		{"forged signature", "aleksandar@example.com", true, true, "/", "did not check out"},
	} {
		idp.email, idp.verified, idp.forge = c.email, c.verified, c.forge
		end, cl := h.signIn(c.next)
		if end == nil || end.Path != "/login" || !strings.Contains(end.Query().Get("error"), c.want) || h.me(cl) != 401 {
			t.Errorf("%s: ended at %v", c.name, end)
		}
	}

	// the return path cannot leave the site
	idp.email, idp.verified, idp.forge = "aleksandar@example.com", true, false
	if end, _ := h.signIn("//evil.example/x"); end == nil || end.Host != strings.TrimPrefix(h.ts.URL, "http://") || end.Path != "/" {
		t.Errorf("open redirect: %v", end)
	}
	// a callback without the browser's cookie is refused
	res, _ := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Get(h.ts.URL + "/api/v1/auth/oidc/callback?code=x&state=y")
	if loc := res.Header.Get("Location"); !strings.HasPrefix(loc, "/login?error=") {
		t.Errorf("cookieless callback → %q", loc)
	}
}

// TestOIDCFromCore covers the zero-configuration path: no OIDC_* env, the
// client Core issued at registration (persisted by the SDK next to the app
// token) drives the button, and a person Core vouches for gets an account.
func TestOIDCFromCore(t *testing.T) {
	idp := newFakeIdP(t, "pocket-movement", "pcs_secret")
	dir := t.TempDir()
	issued, _ := json.Marshal(map[string]string{"issuer": idp.URL, "client_id": "pocket-movement", "client_secret": "pcs_secret"})
	if err := os.WriteFile(dir+"/core.token.oidc.json", issued, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{DataDir: dir, DBPath: dir + "/t.db", AuthMode: "local", Geocoder: "off",
		SessionTTL: time.Hour, CoreURL: idp.URL, OIDCName: "single sign-on", OIDCAutoCreate: true}
	h := newOIDCHarness(t, cfg)

	if status := h.status(); status["oidc"] != true || status["oidc_name"] != "Pocket Core" {
		t.Fatalf("status %v", status)
	}
	// unknown to the app, vouched for by Core → account created, signed in
	idp.email = "household@example.com"
	end, cl := h.signIn("/")
	if end == nil || end.Path != "/" || h.me(cl) != 200 {
		t.Fatalf("core sign-in ended at %v, me=%d", end, h.me(cl))
	}
	if u, err := h.st.GetUserByEmail(context.Background(), "household@example.com"); err != nil || u.Role != "admin" {
		t.Errorf("first Core user should be the admin: %v %v", u, err)
	}

	// without Core and without env there is no button and the routes answer 404 / redirect
	off := newOIDCHarness(t, &config.Config{DataDir: t.TempDir(), DBPath: t.TempDir() + "/t.db", AuthMode: "local", Geocoder: "off", SessionTTL: time.Hour, OIDCName: "single sign-on"})
	if status := off.status(); status["oidc"] != false {
		t.Errorf("status without Core %v", status)
	}
	if res, _ := http.Get(off.ts.URL + "/api/v1/auth/oidc/start"); res.StatusCode != 404 {
		t.Errorf("start without Core: %d", res.StatusCode)
	}

	// explicit OIDC_* env wins over the Core-issued client
	env := newOIDCHarness(t, &config.Config{DataDir: dir, DBPath: dir + "/e.db", AuthMode: "local", Geocoder: "off", SessionTTL: time.Hour,
		CoreURL: idp.URL, OIDCIssuer: "https://id.example", OIDCClientID: "x", OIDCName: "PocketID"})
	if status := env.status(); status["oidc"] != true || status["oidc_name"] != "PocketID" {
		t.Errorf("env precedence %v", status)
	}
}
