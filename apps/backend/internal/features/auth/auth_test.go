package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
	"gorm.io/gorm"

	"github.com/dot5enko/wondercritique/apps/backend/internal/config"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/auth/mailer"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/dbx"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/httpx"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type captureMailer struct {
	mu   sync.Mutex
	sent []mailer.Email
	fail error
}

func (m *captureMailer) Send(_ context.Context, e mailer.Email) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return m.fail
	}
	m.sent = append(m.sent, e)
	return nil
}

var codeRe = regexp.MustCompile(`\b\d{6}\b`)

func (m *captureMailer) lastCode(t *testing.T) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sent) == 0 {
		t.Fatal("no email sent")
	}
	code := codeRe.FindString(m.sent[len(m.sent)-1].Text)
	if code == "" {
		t.Fatalf("no code in %q", m.sent[len(m.sent)-1].Text)
	}
	return code
}

type env struct {
	t      *testing.T
	h      http.Handler
	clock  *fakeClock
	mail   *captureMailer
	tokens *Tokens
	keys   *keys
	mw     *Middleware
	db     *gorm.DB
}

func newEnv(t *testing.T, opts ...fx.Option) *env {
	t.Helper()
	e := &env{t: t, clock: &fakeClock{t: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}, mail: &captureMailer{}}
	cfg := &config.Config{
		Env:       config.EnvDev,
		JWTSecret: strings.Repeat("x", 32),
		JWTIssuer: "test-issuer",
		DBPath:    "file:auth_" + models.NewID() + "?mode=memory&cache=shared",
	}
	var mux *chi.Mux
	app := fxtest.New(t,
		fx.Supply(cfg, slog.New(slog.DiscardHandler)),
		dbx.Module,
		fx.Provide(httpx.NewRouter),
		Module,
		fx.Decorate(func() Clock { return e.clock.Now }),
		fx.Decorate(func() mailer.Mailer { return e.mail }),
		fx.Options(opts...),
		fx.Populate(&mux, &e.tokens, &e.keys, &e.mw, &e.db),
	)
	app.RequireStart()
	t.Cleanup(app.RequireStop)
	e.h = mux
	return e
}

type resp struct {
	code   int
	header http.Header
	body   map[string]any
	raw    string
}

func (e *env) do(method, path, token string, body any) resp {
	e.t.Helper()
	var rd *strings.Reader
	switch b := body.(type) {
	case nil:
		rd = strings.NewReader("")
	case string:
		rd = strings.NewReader(b)
	default:
		js, _ := json.Marshal(b)
		rd = strings.NewReader(string(js))
	}
	r := httptest.NewRequest(method, path, rd)
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	out := resp{code: w.Code, header: w.Header(), raw: w.Body.String()}
	_ = json.Unmarshal(w.Body.Bytes(), &out.body)
	return out
}

func (e *env) expect(r resp, code int) resp {
	e.t.Helper()
	if r.code != code {
		e.t.Fatalf("status = %d, want %d: %s", r.code, code, r.raw)
	}
	return r
}

func (e *env) start(email string) resp {
	e.t.Helper()
	return e.do("POST", "/api/v1/auth/email_code/start", "", map[string]string{"email": email})
}

func (e *env) verify(email, code string) resp {
	e.t.Helper()
	return e.do("POST", "/api/v1/auth/email_code/verify", "", map[string]string{"email": email, "code": code})
}

func (e *env) login(email string) resp {
	e.t.Helper()
	e.expect(e.start(email), 200)
	return e.expect(e.verify(email, e.mail.lastCode(e.t)), 200)
}

// With the dev log mailer (no RESEND_API_KEY) the code comes back in the start response.
func TestEmailCodeDevCodeWithLogMailer(t *testing.T) {
	e := newEnv(t)
	log := slog.New(slog.DiscardHandler)
	p := newEmailCodeProvider(e.db, mailer.NewLog(log), e.keys, e.clock.Now, log)

	res, err := p.Start(t.Context(), json.RawMessage(`{"email":"bob@example.com"}`))
	if err != nil {
		t.Fatal(err)
	}
	code, _ := res["dev_code"].(string)
	if !codeRe.MatchString(code) || res["dev_notice"] == "" {
		t.Fatalf("start result = %v", res)
	}
	if _, err := p.Verify(t.Context(), json.RawMessage(`{"email":"bob@example.com","code":"`+code+`"}`)); err != nil {
		t.Fatalf("verify dev_code: %v", err)
	}
}

func errCode(r resp) string {
	if m, ok := r.body["error"].(map[string]any); ok {
		s, _ := m["code"].(string)
		return s
	}
	return ""
}

func wrongCode(code string) string {
	if code == "000000" {
		return "000001"
	}
	return "000000"
}

func TestEmailCodeFlow(t *testing.T) {
	e := newEnv(t)

	r := e.expect(e.start("  Alice@Example.COM "), 200)
	if r.body["sent"] != true || r.body["expires_in"] != float64(600) {
		t.Fatalf("start body = %s", r.raw)
	}
	if _, ok := r.body["dev_code"]; ok {
		t.Fatalf("dev_code leaked with a real mailer: %s", r.raw)
	}
	if to := e.mail.sent[0].To; to != "alice@example.com" {
		t.Fatalf("mail to = %q", to)
	}

	r = e.expect(e.verify("ALICE@example.com", e.mail.lastCode(t)), 200)
	if r.body["token_type"] != "Bearer" || r.body["expires_in"] != float64(900) {
		t.Fatalf("verify body = %s", r.raw)
	}
	user := r.body["user"].(map[string]any)
	if user["email"] != "alice@example.com" || user["role"] != "user" || user["id"] == "" {
		t.Fatalf("user = %v", user)
	}
	access, refresh := r.body["access_token"].(string), r.body["refresh_token"].(string)

	me := e.expect(e.do("GET", "/api/v1/auth/me", access, nil), 200)
	if me.body["id"] != user["id"] || me.body["email"] != "alice@example.com" {
		t.Fatalf("me = %s", me.raw)
	}
	e.expect(e.do("GET", "/api/v1/auth/me", "", nil), 401)

	// Rotation: the new pair works, the old refresh token is now rotated.
	r2 := e.expect(e.do("POST", "/api/v1/auth/refresh", "", map[string]string{"refresh_token": refresh}), 200)
	refresh2 := r2.body["refresh_token"].(string)
	if refresh2 == refresh || r2.body["access_token"] == access {
		t.Fatal("tokens not rotated")
	}
	e.expect(e.do("GET", "/api/v1/auth/me", r2.body["access_token"].(string), nil), 200)

	// Reuse of the rotated token revokes the whole chain, including refresh2.
	e.expect(e.do("POST", "/api/v1/auth/refresh", "", map[string]string{"refresh_token": refresh}), 401)
	e.expect(e.do("POST", "/api/v1/auth/refresh", "", map[string]string{"refresh_token": refresh2}), 401)

	// Logging in again resolves the same user through the stored identity.
	e.clock.Advance(CodeCooldown)
	r3 := e.login("alice@example.com")
	if r3.body["user"].(map[string]any)["id"] != user["id"] {
		t.Fatal("second login created a new user")
	}
	var n int64
	e.db.Model(&UserIdentity{}).Count(&n)
	if n != 1 {
		t.Fatalf("identities = %d", n)
	}

	refresh3 := r3.body["refresh_token"].(string)
	e.expect(e.do("POST", "/api/v1/auth/logout", "", map[string]string{"refresh_token": refresh3}), 204)
	e.expect(e.do("POST", "/api/v1/auth/refresh", "", map[string]string{"refresh_token": refresh3}), 401)
	e.expect(e.do("POST", "/api/v1/auth/refresh", "", map[string]string{"refresh_token": "nope"}), 401)
}

func TestLinksExistingUserByEmail(t *testing.T) {
	e := newEnv(t)
	u := models.User{Email: "bob@example.com", Role: models.RoleAdmin}
	if err := e.db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	r := e.login("Bob@Example.com")
	got := r.body["user"].(map[string]any)
	if got["id"] != u.ID || got["role"] != "admin" {
		t.Fatalf("user = %v", got)
	}
	c, err := e.tokens.ParseAccess(r.body["access_token"].(string))
	if err != nil || c.Role != "admin" || c.Subject != u.ID || c.Issuer != "test-issuer" || c.ID == "" {
		t.Fatalf("claims = %+v, err = %v", c, err)
	}
}

func TestLogoutDoesNotRevokeOtherSessions(t *testing.T) {
	e := newEnv(t)
	a := e.login("a@example.com")
	e.clock.Advance(CodeCooldown)
	b := e.login("a@example.com")

	ra := a.body["refresh_token"].(string)
	e.expect(e.do("POST", "/api/v1/auth/logout", "", map[string]string{"refresh_token": ra}), 204)
	e.expect(e.do("POST", "/api/v1/auth/refresh", "", map[string]string{"refresh_token": ra}), 401)
	// Retrying a logged-out token is not theft: the other session survives.
	e.expect(e.do("POST", "/api/v1/auth/refresh", "", map[string]string{"refresh_token": b.body["refresh_token"].(string)}), 200)
}

func TestRefreshExpiry(t *testing.T) {
	e := newEnv(t)
	r := e.login("a@example.com")
	e.clock.Advance(RefreshTTL)
	e.expect(e.do("POST", "/api/v1/auth/refresh", "", map[string]string{"refresh_token": r.body["refresh_token"].(string)}), 401)
}

func TestDeletedUserCannotLogIn(t *testing.T) {
	e := newEnv(t)
	u := models.User{Email: "Gone@Example.com "}
	if err := e.db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	if u.Email != "gone@example.com" || u.Role != models.RoleUser {
		t.Fatalf("user not normalized: %+v", u)
	}
	if err := e.db.Delete(&u).Error; err != nil {
		t.Fatal(err)
	}
	e.expect(e.start("gone@example.com"), 200)
	e.expect(e.verify("gone@example.com", e.mail.lastCode(t)), 403)
}

func TestVerifyAttemptLimit(t *testing.T) {
	e := newEnv(t)
	e.expect(e.start("a@example.com"), 200)
	code := e.mail.lastCode(t)
	for i := range CodeMaxAttempts {
		r := e.expect(e.verify("a@example.com", wrongCode(code)), 401)
		if msg := r.body["error"].(map[string]any)["message"]; msg != "invalid or expired code" {
			t.Fatalf("attempt %d: message = %v", i, msg)
		}
	}
	e.expect(e.verify("a@example.com", code), 401) // exhausted, even with the right code
}

func TestCodeExpiry(t *testing.T) {
	e := newEnv(t)
	e.expect(e.start("a@example.com"), 200)
	code := e.mail.lastCode(t)
	e.clock.Advance(CodeTTL)
	e.expect(e.verify("a@example.com", code), 401)
}

func TestUnknownEmailLooksLikeWrongCode(t *testing.T) {
	e := newEnv(t)
	r := e.expect(e.verify("ghost@example.com", "123456"), 401)
	if msg := r.body["error"].(map[string]any)["message"]; msg != "invalid or expired code" {
		t.Fatalf("message = %v", msg)
	}
}

func TestResendCooldown(t *testing.T) {
	e := newEnv(t)
	e.expect(e.start("a@example.com"), 200)
	first := e.mail.lastCode(t)

	e.clock.Advance(20 * time.Second)
	r := e.expect(e.start("a@example.com"), 429)
	if r.header.Get("Retry-After") != "40" {
		t.Fatalf("Retry-After = %q", r.header.Get("Retry-After"))
	}
	if f := r.body["error"].(map[string]any)["fields"].(map[string]any); f["retry_after"] != "40" {
		t.Fatalf("fields = %v", f)
	}
	if len(e.mail.sent) != 1 {
		t.Fatal("email sent during cooldown")
	}
	e.expect(e.start("b@example.com"), 200) // cooldown is per email

	e.clock.Advance(40 * time.Second)
	e.expect(e.start("a@example.com"), 200)
	second := e.mail.lastCode(t)
	if first != second {
		e.expect(e.verify("a@example.com", first), 401) // superseded
	}
	e.expect(e.verify("a@example.com", second), 200)
}

func TestMailFailureDoesNotBlockRetry(t *testing.T) {
	e := newEnv(t)
	e.mail.fail = errors.New("smtp down")
	e.expect(e.start("a@example.com"), 503)
	e.mail.fail = nil
	e.expect(e.start("a@example.com"), 200)
}

func TestValidationAndUnknownProvider(t *testing.T) {
	e := newEnv(t)
	e.expect(e.do("POST", "/api/v1/auth/nope/start", "", map[string]string{"email": "a@example.com"}), 404)
	e.expect(e.do("POST", "/api/v1/auth/nope/verify", "", map[string]string{}), 404)
	if r := e.expect(e.start("not-an-email"), 400); errCode(r) != "invalid" {
		t.Fatal(r.raw)
	}
	e.expect(e.verify("a@example.com", "12ab56"), 400)
	e.expect(e.do("POST", "/api/v1/auth/email_code/start", "", `{"email":"a@example.com","x":1}`), 400)
	e.expect(e.do("POST", "/api/v1/auth/email_code/start", "", `{"email":`), 400)
}

func TestRateLimit(t *testing.T) {
	e := newEnv(t, fx.Decorate(func() RateLimit { return RateLimit{Every: time.Minute, Burst: 2} }))
	e.expect(e.start("a@example.com"), 200)
	e.expect(e.verify("a@example.com", "000000"), 401)
	r := e.expect(e.start("b@example.com"), 429)
	if r.header.Get("Retry-After") != "60" {
		t.Fatalf("Retry-After = %q", r.header.Get("Retry-After"))
	}
	e.clock.Advance(time.Minute)
	e.expect(e.start("b@example.com"), 200)
}

func sign(t *testing.T, m jwt.SigningMethod, key any, c jwt.Claims) string {
	t.Helper()
	s, err := jwt.NewWithClaims(m, c).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAccessTokenValidation(t *testing.T) {
	e := newEnv(t)
	r := e.login("a@example.com")
	valid := r.body["access_token"].(string)
	uid := r.body["user"].(map[string]any)["id"].(string)
	now := e.clock.Now()
	claims := func(mut func(*Claims)) *Claims {
		c := &Claims{Role: "user", RegisteredClaims: jwt.RegisteredClaims{
			Subject: uid, Issuer: "test-issuer", ID: "j",
			IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)),
		}}
		if mut != nil {
			mut(c)
		}
		return c
	}

	e.expect(e.do("GET", "/api/v1/auth/me", sign(t, jwt.SigningMethodHS256, e.keys.jwt, claims(nil)), nil), 200)

	bad := map[string]string{
		"garbage":       "not.a.jwt",
		"wrong alg":     sign(t, jwt.SigningMethodHS384, e.keys.jwt, claims(nil)),
		"alg none":      sign(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, claims(nil)),
		"wrong key":     sign(t, jwt.SigningMethodHS256, []byte(strings.Repeat("y", 32)), claims(nil)),
		"raw secret":    sign(t, jwt.SigningMethodHS256, []byte(strings.Repeat("x", 32)), claims(nil)),
		"wrong issuer":  sign(t, jwt.SigningMethodHS256, e.keys.jwt, claims(func(c *Claims) { c.Issuer = "evil" })),
		"no subject":    sign(t, jwt.SigningMethodHS256, e.keys.jwt, claims(func(c *Claims) { c.Subject = "" })),
		"no exp":        sign(t, jwt.SigningMethodHS256, e.keys.jwt, claims(func(c *Claims) { c.ExpiresAt = nil })),
		"expired":       sign(t, jwt.SigningMethodHS256, e.keys.jwt, claims(func(c *Claims) { c.ExpiresAt = jwt.NewNumericDate(now.Add(-31 * time.Second)) })),
		"issued future": sign(t, jwt.SigningMethodHS256, e.keys.jwt, claims(func(c *Claims) { c.IssuedAt = jwt.NewNumericDate(now.Add(time.Minute)) })),
	}
	for name, tok := range bad {
		t.Run(name, func(t *testing.T) {
			r := e.expect(e.do("GET", "/api/v1/auth/me", tok, nil), 401)
			if r.header.Get("WWW-Authenticate") != "Bearer" {
				t.Fatal("missing WWW-Authenticate")
			}
		})
	}

	// Within leeway still OK; the issued token expires after 15m + leeway.
	e.clock.Advance(AccessTTL + 20*time.Second)
	e.expect(e.do("GET", "/api/v1/auth/me", valid, nil), 200)
	e.clock.Advance(15 * time.Second)
	e.expect(e.do("GET", "/api/v1/auth/me", valid, nil), 401)

	// Malformed header.
	req := httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	req.Header.Set("Authorization", "Basic abc")
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("basic auth: %d", w.Code)
	}
}

func TestMiddlewareAndRole(t *testing.T) {
	e := newEnv(t)
	issue := func(role string) string {
		tok, err := e.tokens.IssueAccess(&models.User{Base: models.Base{ID: "u-" + role}, Role: role})
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	r := chi.NewRouter()
	echo := func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("uid=" + UserID(r.Context()))) }
	r.With(e.mw.Required, Role("admin")).Get("/admin", echo)
	r.With(e.mw.Optional).Get("/opt", echo)
	r.With(Role("user")).Get("/role-only", echo)

	call := func(path, tok string) (int, string) {
		req := httptest.NewRequest("GET", path, nil)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	tests := []struct {
		path, tok string
		code      int
		body      string
	}{
		{"/admin", "", 401, ""},
		{"/admin", issue("user"), 403, ""},
		{"/admin", issue("admin"), 200, "uid=u-admin"},
		{"/opt", "", 200, "uid="},
		{"/opt", issue("user"), 200, "uid=u-user"},
		{"/opt", "bogus", 401, ""},
		{"/role-only", "", 401, ""},
	}
	for _, tt := range tests {
		code, body := call(tt.path, tt.tok)
		if code != tt.code || (tt.body != "" && body != tt.body) {
			t.Errorf("%s (tok=%v): %d %q, want %d %q", tt.path, tt.tok != "", code, body, tt.code, tt.body)
		}
	}

	ctx := WithClaims(context.Background(), &Claims{RegisteredClaims: jwt.RegisteredClaims{Subject: "x"}})
	if UserID(ctx) != "x" || UserID(context.Background()) != "" {
		t.Fatal("UserID helpers")
	}
}

func TestMailerSelection(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	if _, err := mailer.New(&config.Config{Env: config.EnvProd}, log); err == nil {
		t.Fatal("prod without RESEND_API_KEY must fail")
	}
	if _, err := mailer.New(&config.Config{Env: config.EnvDev, ResendAPIKey: "re_x"}, log); err == nil {
		t.Fatal("RESEND_API_KEY without MAIL_FROM must fail")
	}
	if m, err := mailer.New(&config.Config{Env: config.EnvDev}, log); err != nil {
		t.Fatal(err)
	} else if _, ok := m.(*mailer.LogMailer); !ok {
		t.Fatalf("dev mailer = %T", m)
	}
	if m, err := mailer.New(&config.Config{Env: config.EnvProd, ResendAPIKey: "re_x", MailFrom: "a@b.c"}, log); err != nil {
		t.Fatal(err)
	} else if _, ok := m.(*mailer.ResendMailer); !ok {
		t.Fatalf("prod mailer = %T", m)
	}
}

func TestEphemeralSecretInDev(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	k1, err := newKeys(&config.Config{Env: config.EnvDev}, log)
	if err != nil {
		t.Fatal(err)
	}
	k2, _ := newKeys(&config.Config{Env: config.EnvDev}, log)
	if len(k1.jwt) != 32 || string(k1.jwt) == string(k2.jwt) || string(k1.jwt) == string(k1.code) {
		t.Fatal("ephemeral keys must be random and per-purpose")
	}
	if _, err := newKeys(&config.Config{Env: config.EnvProd}, log); err == nil {
		t.Fatal("prod without secret must fail")
	}
}

func TestNewCodeFormat(t *testing.T) {
	for range 200 {
		if c := newCode(); len(c) != 6 || strings.Trim(c, "0123456789") != "" {
			t.Fatalf("code %q", c)
		}
	}
}
