package gateway

import (
	"bytes"
	"context"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type testSession struct {
	cookie *http.Cookie
	csrf   string
	server string
}

func (s testSession) call(t *testing.T, method, path, body string) (int, []byte) {
	t.Helper()
	r, _ := http.NewRequest(method, s.server+"/admin/api/"+path, strings.NewReader(body))
	r.AddCookie(s.cookie)
	r.Header.Set("X-CSRF-Token", s.csrf)
	r.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}
func fixtureSession(t *testing.T, a *App, server, id string) testSession {
	t.Helper()
	token := randomToken(32)
	csrf := randomToken(32)
	if _, err := a.DB.Exec(context.Background(), "INSERT INTO sessions(token_hash,csrf,expires_at,user_id) VALUES($1,$2,now()+interval '1 hour',$3)", hash(token), csrf, id); err != nil {
		t.Fatal(err)
	}
	return testSession{&http.Cookie{Name: "gyai_session", Value: token}, csrf, server}
}
func loginChallenge(t *testing.T, server, email, password string) (map[string]any, *http.Cookie) {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"username": email, "password": password})
	r, _ := http.NewRequest("POST", server+"/admin/api/login", bytes.NewReader(raw))
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var v map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&v)
	if resp.StatusCode != 200 || v["mfa_required"] != true {
		t.Fatal("password did not create MFA challenge", resp.StatusCode, v)
	}
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "gyai_session" {
			t.Fatal("session granted before OTP")
		}
		if c.Name == "gyai_challenge" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("missing challenge cookie")
	}
	return v, cookie
}
func finishChallenge(t *testing.T, server string, v map[string]any, cookie *http.Cookie, code string, csrf bool) (int, map[string]any, *http.Cookie) {
	t.Helper()
	r, _ := http.NewRequest("POST", server+"/admin/api/mfa", strings.NewReader(fmt.Sprintf(`{"otp":%q}`, code)))
	r.AddCookie(cookie)
	if csrf {
		r.Header.Set("X-CSRF-Token", v["csrf"].(string))
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&result)
	var session *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "gyai_session" {
			session = c
		}
	}
	return resp.StatusCode, result, session
}
func testAdminLogin(t *testing.T, server string) testSession {
	t.Helper()
	v, cookie := loginChallenge(t, server, "admin", "test-password-123")
	secret, _ := v["secret"].(string)
	if secret == "" {
		t.Fatal("fixture administrator already bound")
	}
	status, result, session := finishChallenge(t, server, v, cookie, otpCode(secret, time.Now().Unix()/30), true)
	if status != 200 || session == nil {
		t.Fatal("fixture MFA failed", status, result)
	}
	return testSession{session, result["csrf"].(string), server}
}
func TestTOTPStandardVectorsAndReplay(t *testing.T) {
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("12345678901234567890"))
	for i, want := range []string{"755224", "287082", "359152", "969429", "338314", "254676", "287922", "162583", "399871", "520489"} {
		if got := otpCode(secret, int64(i)); got != want {
			t.Fatal(i, got, want)
		}
	}
	now := time.Unix(59, 0)
	if step, ok := verifyOTP(secret, "287082", now, -1); !ok || step != 1 {
		t.Fatal(step, ok)
	}
	if _, ok := verifyOTP(secret, "287082", now, 1); ok {
		t.Fatal("OTP replay accepted")
	}
	for _, bad := range []string{"", "12345", "0000000", "12345x"} {
		if _, ok := verifyOTP(secret, bad, now, -1); ok {
			t.Fatal("bad OTP accepted")
		}
	}
}
func TestMandatoryMFAEnrollmentAndAccountManagement(t *testing.T) {
	a, srv := testApp(t)
	ctx := context.Background()
	v, challenge := loginChallenge(t, srv.URL, "admin", "test-password-123")
	s := testSession{challenge, v["csrf"].(string), srv.URL}
	if status, _ := s.call(t, "GET", "session", ""); status != 401 {
		t.Fatal("challenge granted access", status)
	}
	secret := v["secret"].(string)
	code := otpCode(secret, time.Now().Unix()/30)
	if status, _, _ := finishChallenge(t, srv.URL, v, challenge, code, false); status != 403 {
		t.Fatal("MFA CSRF bypass", status)
	}
	if status, _, _ := finishChallenge(t, srv.URL, v, challenge, "bad", true); status != 401 {
		t.Fatal("wrong OTP accepted")
	}
	status, result, cookie := finishChallenge(t, srv.URL, v, challenge, code, true)
	if status != 200 || cookie == nil {
		t.Fatal(status, result)
	}
	admin := testSession{cookie, result["csrf"].(string), srv.URL}
	var encrypted []byte
	if err := a.DB.QueryRow(ctx, "SELECT totp_secret FROM users WHERE id='bootstrap'").Scan(&encrypted); err != nil || bytes.Contains(encrypted, []byte(secret)) {
		t.Fatal("OTP plaintext persisted", err)
	}
	v2, c2 := loginChallenge(t, srv.URL, "admin", "test-password-123")
	if v2["secret"] != nil || v2["enroll"] != false {
		t.Fatal("bound OTP leaked")
	}
	if status, _, _ := finishChallenge(t, srv.URL, v2, c2, code, true); status != 401 {
		t.Fatal("used OTP accepted")
	}
	status, b := admin.call(t, "POST", "users", `{"email":"new@example.com"}`)
	if status != 201 {
		t.Fatal(status, string(b))
	}
	var created map[string]string
	_ = json.Unmarshal(b, &created)
	if len(created["password"]) != 14 || created["role"] != "user" {
		t.Fatal("password/role default incorrect")
	}
	var digest string
	_ = a.DB.QueryRow(ctx, "SELECT password_hash FROM users WHERE id=$1", created["id"]).Scan(&digest)
	if bcrypt.CompareHashAndPassword([]byte(digest), []byte(created["password"])) != nil {
		t.Fatal("generated password hash invalid")
	}
	status, b = admin.call(t, "GET", "users", "")
	if status != 200 || bytes.Contains(b, []byte(created["password"])) || bytes.Contains(b, []byte(secret)) {
		t.Fatal("user list leaks credentials", status)
	}
	nv, nc := loginChallenge(t, srv.URL, created["email"], created["password"])
	if nv["enroll"] != true {
		t.Fatal("new user OTP not mandatory")
	}
	_, _, _ = finishChallenge(t, srv.URL, nv, nc, otpCode(nv["secret"].(string), time.Now().Unix()/30), true)
	ordinarySession := fixtureSession(t, a, srv.URL, created["id"])
	if status, _ := ordinarySession.call(t, "POST", "users", `{"email":"evil@example.com","role":"admin"}`); status != 403 {
		t.Fatal("user created account", status)
	}
	if status, _ := admin.call(t, "POST", "users/"+created["id"]+"/reset_otp", "{}"); status != 200 {
		t.Fatal(status)
	}
	if status, _ := ordinarySession.call(t, "GET", "session", ""); status != 401 {
		t.Fatal("OTP reset left session active")
	}
	nv, _ = loginChallenge(t, srv.URL, created["email"], created["password"])
	if nv["enroll"] != true {
		t.Fatal("reset OTP did not require rebinding")
	}
	if status, _ := admin.call(t, "PATCH", "users/bootstrap", `{"enabled":false}`); status != 409 {
		t.Fatal("last admin disabled", status)
	}
	var leaks bool
	_ = a.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM audit_logs WHERE resource_id=$1 AND (action LIKE $2 OR resource LIKE $2))", created["id"], "%"+created["password"]+"%").Scan(&leaks)
	if leaks {
		t.Fatal("password in audit")
	}
}
func seedOrdinary(t *testing.T, a *App, id string) {
	t.Helper()
	if _, err := a.DB.Exec(context.Background(), "INSERT INTO users(id,email,password_hash) VALUES($1,$2,'fixture-only')", id, id+"@example.com"); err != nil {
		t.Fatal(err)
	}
}
func seedPricedModel(t *testing.T, a *App) {
	t.Helper()
	if _, err := a.DB.Exec(context.Background(), `INSERT INTO model_pricing(platform,model,input_price,output_price,source_current,long_context_policy) VALUES('openai','test-model',1,2,true,'none')`); err != nil {
		t.Fatal(err)
	}
}
func TestOrdinaryUserIsolationAndCapturePrivacy(t *testing.T) {
	a, srv := testApp(t)
	seed(t, a)
	seedOrdinary(t, a, "alice")
	seedOrdinary(t, a, "bob")
	seedPricedModel(t, a)
	ctx := context.Background()
	_, err := a.DB.Exec(ctx, "UPDATE gateway_keys SET user_id=CASE WHEN id='g1' THEN 'alice' ELSE 'bob' END,store_input=true,store_output=true")
	if err != nil {
		t.Fatal(err)
	}
	alice, bob := fixtureSession(t, a, srv.URL, "alice"), fixtureSession(t, a, srv.URL, "bob")
	for _, path := range []string{"upstream_keys", "upstream_keys/k1/model_catalog", "settings", "storage/test", "pricing", "dashboard", "audit", "users", "cooldowns"} {
		method := "GET"
		if path == "storage/test" {
			method = "POST"
		}
		if status, _ := alice.call(t, method, path, "{}"); status != 403 {
			t.Fatal("restricted endpoint available", path, status)
		}
	}
	for _, tc := range []struct{ method, path, body string }{{"PATCH", "gateway_keys/g2", `{"enabled":false}`}, {"DELETE", "gateway_keys/g2", ""}, {"POST", "gateway_keys/g2/reveal", "{}"}, {"POST", "gateway_keys/g2/rotate", "{}"}, {"GET", "gateway_keys/g2/spending", ""}} {
		if status, _ := alice.call(t, tc.method, tc.path, tc.body); status != 404 {
			t.Fatal("other user's key reachable", tc.path, status)
		}
	}
	for _, body := range []string{`{"store_input":false}`, `{"store_output":true}`} {
		if status, _ := alice.call(t, "PATCH", "gateway_keys/g1", body); status != 403 {
			t.Fatal("user altered capture policy", status)
		}
	}
	_, err = a.DB.Exec(ctx, `INSERT INTO request_logs(id,gateway_key_id,upstream_key_id,model,method,endpoint,ip,status,duration_ms,total_cost) VALUES('alice-log','g1','k1','test-model','POST','/v1/responses','127.0.0.1',200,1,0.01),('bob-log','g2','k2','test-model','POST','/v1/responses','127.0.0.1',200,1,0.02)`)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"gateway_keys", "logs", "session"} {
		status, b := alice.call(t, "GET", path, "")
		if status != 200 {
			t.Fatal(path, status, string(b))
		}
		for _, forbidden := range []string{"store_input", "store_output", "storage_enabled", "input_path", "output_path", "input_status", "output_status", "upstream_key_id", "upstream_name", "bob-log", "\"g2\""} {
			if bytes.Contains(b, []byte(forbidden)) {
				t.Fatal("private metadata leaked", path, forbidden, string(b))
			}
		}
	}
	for _, s := range []testSession{alice, bob} {
		status, b := s.call(t, "GET", "spending", "")
		if status != 200 {
			t.Fatal(status, string(b))
		}
		var data struct {
			Summary struct {
				Requests int `json:"requests"`
			}
		}
		_ = json.Unmarshal(b, &data)
		if data.Summary.Requests != 1 {
			t.Fatal("other user's spending counted", string(b))
		}
	}
	if status, b := alice.call(t, "GET", "models", ""); status != 200 || !bytes.Contains(b, []byte("test-model")) {
		t.Fatal("priced models unavailable", status, string(b))
	}
	_, _ = a.DB.Exec(ctx, "UPDATE model_pricing SET source_current=false")
	if status, b := alice.call(t, "GET", "models", ""); status != 200 || bytes.Contains(b, []byte("test-model")) {
		t.Fatal("unpriced models leaked", status, string(b))
	}
}
func TestUserKeyLimitConcurrencyAndIPRules(t *testing.T) {
	a, srv := testApp(t)
	seed(t, a)
	seedOrdinary(t, a, "alice")
	seedPricedModel(t, a)
	s := fixtureSession(t, a, srv.URL, "alice")
	for _, ip := range []string{"0.0.0.0", "0.0.0.0/0", "::", "::/0", "0.0.0.0/32", "::/128"} {
		status, _ := s.call(t, "POST", "gateway_keys", fmt.Sprintf(`{"name":"bad","models":["test-model"],"cidrs":[%q],"expires_in_days":30}`, ip))
		if status != 400 {
			t.Fatal("unspecified or wildcard IP accepted", ip, status)
		}
	}
	if status, _ := s.call(t, "POST", "gateway_keys", `{"name":"bad-model","models":["unpriced-model"],"cidrs":["127.0.0.1"],"expires_in_days":30}`); status != 400 {
		t.Fatal("unpriced model accepted")
	}
	var accepted, limited atomic.Int64
	var mu sync.Mutex
	ids := []string{}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			status, b := s.call(t, "POST", "gateway_keys", fmt.Sprintf(`{"name":"key-%d","models":["test-model"],"cidrs":["192.0.2.0/24","2001:db8::/64"],"expires_in_days":30}`, i))
			if status == 201 {
				accepted.Add(1)
				var data map[string]string
				_ = json.Unmarshal(b, &data)
				mu.Lock()
				ids = append(ids, data["id"])
				mu.Unlock()
			} else if status == 409 {
				limited.Add(1)
			} else {
				t.Error(status, string(b))
			}
		}(i)
	}
	wg.Wait()
	if accepted.Load() != 3 || limited.Load() != 5 {
		t.Fatal("concurrent key limit bypass", accepted.Load(), limited.Load())
	}
	if status, _ := s.call(t, "PATCH", "gateway_keys/"+ids[0], `{"enabled":false}`); status != 200 {
		t.Fatal(status)
	}
	if status, _ := s.call(t, "POST", "gateway_keys", `{"name":"fourth","models":["test-model"],"cidrs":["127.0.0.1"],"expires_in_days":30}`); status != 409 {
		t.Fatal("disabled key did not count")
	}
	if status, _ := s.call(t, "DELETE", "gateway_keys/"+ids[0], ""); status != 200 {
		t.Fatal(status)
	}
	if status, b := s.call(t, "POST", "gateway_keys", `{"name":"replacement","models":["test-model"],"cidrs":["127.0.0.1","2001:db8::1"],"expires_in_days":30}`); status != 201 {
		t.Fatal("deleted key slot not freed", status, string(b))
	}
}

func TestDisabledOwnerAndPricingEnforcedDuringCalls(t *testing.T) {
	a, srv := testApp(t)
	seed(t, a)
	seedOrdinary(t, a, "alice")
	seedPricedModel(t, a)
	ctx := context.Background()
	_, err := a.DB.Exec(ctx, "UPDATE gateway_keys SET user_id='alice' WHERE id='g1'")
	if err != nil {
		t.Fatal(err)
	}
	user := fixtureSession(t, a, srv.URL, "alice")
	admin := fixtureSession(t, a, srv.URL, "bootstrap")
	var count atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		fmt.Fprint(w, `{"usage":{"prompt_tokens":10,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":0}}}`)
	}))
	defer up.Close()
	a.Config.UpstreamURL = up.URL
	if status, _ := request(t, srv.URL, "POST", "/v1/chat/completions", "gyai-g1", `{"model":"test-model"}`); status != 200 {
		t.Fatal(status)
	}
	_, _ = a.DB.Exec(ctx, "UPDATE model_pricing SET source_current=false")
	if status, _ := request(t, srv.URL, "POST", "/v1/chat/completions", "gyai-g1", `{"model":"test-model"}`); status != 403 {
		t.Fatal("unpriced model called", status)
	}
	status, b := request(t, srv.URL, "GET", "/v1/models", "gyai-g1", "")
	if status != 200 || bytes.Contains(b, []byte("test-model")) {
		t.Fatal("API model catalog leaked unpriced model", status, string(b))
	}
	_, _ = a.DB.Exec(ctx, "UPDATE model_pricing SET source_current=true")
	if status, _ := admin.call(t, "PATCH", "users/alice", `{"enabled":false}`); status != 200 {
		t.Fatal(status)
	}
	if status, _ := user.call(t, "GET", "session", ""); status != 401 {
		t.Fatal("disabled user session active", status)
	}
	if status, _ := request(t, srv.URL, "POST", "/v1/chat/completions", "gyai-g1", `{"model":"test-model"}`); status != 401 {
		t.Fatal("disabled owner key active", status)
	}
	if count.Load() != 1 {
		t.Fatal("unauthorized request reached upstream", count.Load())
	}
}

func TestMFAChallengeExpiryAttemptsAndCredentialReset(t *testing.T) {
	a, srv := testApp(t)
	admin := fixtureSession(t, a, srv.URL, "bootstrap")
	ctx := context.Background()
	status, b := admin.call(t, "POST", "users", `{"email":"new@example.com"}`)
	if status != 201 {
		t.Fatal(status, string(b))
	}
	var created map[string]string
	_ = json.Unmarshal(b, &created)
	v, c := loginChallenge(t, srv.URL, created["email"], created["password"])
	for i := 0; i < 5; i++ {
		if status, _, _ := finishChallenge(t, srv.URL, v, c, "wrong", true); status != 401 {
			t.Fatal(status)
		}
	}
	if status, result, _ := finishChallenge(t, srv.URL, v, c, otpCode(v["secret"].(string), time.Now().Unix()/30), true); status != 401 || result["error"].(map[string]any)["code"] != "challenge_expired" {
		t.Fatal("challenge attempt limit bypass", status)
	}
	v, c = loginChallenge(t, srv.URL, created["email"], created["password"])
	_, _ = a.DB.Exec(ctx, "UPDATE login_challenges SET expires_at=now()-interval '1 second' WHERE token_hash=$1", hash(c.Value))
	if status, _, _ := finishChallenge(t, srv.URL, v, c, otpCode(v["secret"].(string), time.Now().Unix()/30), true); status != 401 {
		t.Fatal("expired challenge accepted")
	}
	v, c = loginChallenge(t, srv.URL, created["email"], created["password"])
	user := fixtureSession(t, a, srv.URL, created["id"])
	status, b = admin.call(t, "POST", "users/"+created["id"]+"/reset_password", "{}")
	if status != 200 {
		t.Fatal(status, string(b))
	}
	var reset map[string]any
	_ = json.Unmarshal(b, &reset)
	if len(reset["password"].(string)) != 14 {
		t.Fatal("reset password length")
	}
	if status, _ := user.call(t, "GET", "session", ""); status != 401 {
		t.Fatal("password reset kept session")
	}
	if status, _, _ := finishChallenge(t, srv.URL, v, c, otpCode(v["secret"].(string), time.Now().Unix()/30), true); status != 401 {
		t.Fatal("pre-reset challenge accepted")
	}
	if status, _ := request(t, srv.URL, "POST", "/admin/api/login", "", fmt.Sprintf(`{"email":%q,"password":%q}`, created["email"], created["password"])); status != 401 {
		t.Fatal("old password accepted")
	}
	loginChallenge(t, srv.URL, created["email"], reset["password"].(string))
}

func TestPasswordChangeRequiresFreshOTPAndRevokesSessions(t *testing.T) {
	a, srv := testApp(t)
	ctx := context.Background()
	v, c := loginChallenge(t, srv.URL, "admin", "test-password-123")
	secret := v["secret"].(string)
	status, result, cookie := finishChallenge(t, srv.URL, v, c, otpCode(secret, time.Now().Unix()/30), true)
	if status != 200 {
		t.Fatal(status)
	}
	s := testSession{cookie, result["csrf"].(string), srv.URL}
	if status, _ := s.call(t, "POST", "account/password", `{"current_password":"test-password-123","password":"replacement-password","otp":"bad"}`); status != 401 {
		t.Fatal("password changed without OTP")
	}
	if status, _ := s.call(t, "POST", "account/password", fmt.Sprintf(`{"current_password":"test-password-123","password":"replacement-password","otp":%q}`, otpCode(secret, time.Now().Unix()/30+1))); status != 200 {
		t.Fatal("password change failed", status)
	}
	if status, _ := s.call(t, "GET", "session", ""); status != 401 {
		t.Fatal("password change did not revoke session")
	}
	var digest string
	_ = a.DB.QueryRow(ctx, "SELECT password_hash FROM users WHERE id='bootstrap'").Scan(&digest)
	if bcrypt.CompareHashAndPassword([]byte(digest), []byte("replacement-password")) != nil {
		t.Fatal("new password hash invalid")
	}
	loginChallenge(t, srv.URL, "admin", "replacement-password")
}

func TestConcurrentMFARejectsCodeReplay(t *testing.T) {
	a, srv := testApp(t)
	ctx := context.Background()
	secret := newOTPSecret()
	encrypted, _ := seal(a.Config.MasterKey, secret)
	if _, err := a.DB.Exec(ctx, "UPDATE users SET totp_secret=$1 WHERE id='bootstrap'", encrypted); err != nil {
		t.Fatal(err)
	}
	v1, c1 := loginChallenge(t, srv.URL, "admin", "test-password-123")
	v2, c2 := loginChallenge(t, srv.URL, "admin", "test-password-123")
	code := otpCode(secret, time.Now().Unix()/30)
	var wg sync.WaitGroup
	var success, rejected atomic.Int64
	for _, pair := range []struct {
		v map[string]any
		c *http.Cookie
	}{{v1, c1}, {v2, c2}} {
		wg.Add(1)
		go func(v map[string]any, c *http.Cookie) {
			defer wg.Done()
			status, _, _ := finishChallenge(t, srv.URL, v, c, code, true)
			if status == 200 {
				success.Add(1)
			} else if status == 401 {
				rejected.Add(1)
			} else {
				t.Error("unexpected concurrent MFA result", status)
			}
		}(pair.v, pair.c)
	}
	wg.Wait()
	if success.Load() != 1 || rejected.Load() != 1 {
		t.Fatal("concurrent OTP replay", success.Load(), rejected.Load())
	}
}
func TestConcurrentAdministratorChangesPreserveOneAdmin(t *testing.T) {
	a, srv := testApp(t)
	ctx := context.Background()
	_, err := a.DB.Exec(ctx, "INSERT INTO users(id,email,password_hash,role) VALUES('second','second@example.com','fixture','admin')")
	if err != nil {
		t.Fatal(err)
	}
	first, second := fixtureSession(t, a, srv.URL, "bootstrap"), fixtureSession(t, a, srv.URL, "second")
	var wg sync.WaitGroup
	var success, rejected atomic.Int64
	for _, pair := range []struct {
		s  testSession
		id string
	}{{first, "bootstrap"}, {second, "second"}} {
		wg.Add(1)
		go func(s testSession, id string) {
			defer wg.Done()
			status, b := s.call(t, "PATCH", "users/"+id, `{"enabled":false}`)
			if status == 200 {
				success.Add(1)
			} else if status == 409 {
				rejected.Add(1)
			} else {
				t.Error(status, string(b))
			}
		}(pair.s, pair.id)
	}
	wg.Wait()
	if success.Load() != 1 || rejected.Load() != 1 {
		t.Fatal("last admin guard failed", success.Load(), rejected.Load())
	}
	var n int
	_ = a.DB.QueryRow(ctx, "SELECT count(*) FROM users WHERE enabled AND role='admin'").Scan(&n)
	if n != 1 {
		t.Fatal(n)
	}
}
