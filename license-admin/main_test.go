package main

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewLicenseCodeFormat(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 200; i++ {
		code, err := newLicenseCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(code) != 19 || strings.Count(code, "-") != 3 {
			t.Fatalf("code %q should look like XXXX-XXXX-XXXX-XXXX", code)
		}
		for _, r := range strings.ReplaceAll(code, "-", "") {
			if !strings.ContainsRune(codeAlphabet, r) {
				t.Fatalf("code %q contains %q, which is outside the alphabet", code, r)
			}
		}
		if seen[code] {
			t.Fatalf("duplicate code generated: %q", code)
		}
		seen[code] = true
	}
}

func TestNormalizeCode(t *testing.T) {
	want := "ABCD-EFGH-JKLM-NPQR"
	cases := []string{
		"ABCD-EFGH-JKLM-NPQR",
		"abcdefghjklmnpqr",
		"abcd efgh jklm npqr",
		"  ABCD-EFGH-JKLM-NPQR  ",
	}
	for _, c := range cases {
		if got := normalizeCode(c); got != want {
			t.Fatalf("normalizeCode(%q) = %q, want %q", c, got, want)
		}
	}
	if got := normalizeCode("too-short"); got != "" {
		t.Fatalf("normalizeCode(too-short) = %q, want empty", got)
	}
}

func newTestApp(t *testing.T) *app {
	t.Helper()
	templates, err := template.ParseFS(webFS, "web/*.html")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	store, err := newRecordStore(filepath.Join(dir, "records.json"))
	if err != nil {
		t.Fatal(err)
	}
	signKey, err := loadOrCreateSigner(filepath.Join(dir, "signing.key"))
	if err != nil {
		t.Fatal(err)
	}
	return &app{
		templates:    templates,
		username:     "admin",
		password:     "secret123",
		records:      store,
		sessions:     newSessionStore(time.Hour),
		signer:       signKey,
		loginLimiter: newAttemptLimiter(5, time.Minute, time.Minute),
		bindLimiter:  newWindowLimiter(100, time.Minute),
		xlsxPath:     filepath.Join(dir, "授权码.xlsx"),
	}
}

func TestGenerateWritesTable(t *testing.T) {
	a := newTestApp(t)
	request := httptest.NewRequest(http.MethodPost, "/admin/api/generate", strings.NewReader(`{"count":3,"note":"客户A"}`))
	response := httptest.NewRecorder()

	a.handleGenerate(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", response.Code, response.Body.String())
	}
	var payload struct {
		Codes []string `json:"codes"`
		Table string   `json:"table"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Codes) != 3 {
		t.Fatalf("codes = %v, want 3", payload.Codes)
	}
	if _, err := os.Stat(a.xlsxPath); err != nil {
		t.Fatalf("table was not written: %v", err)
	}
	if records := a.records.snapshot(); len(records) != 3 || records[0].Note != "客户A" {
		t.Fatalf("records = %+v", records)
	}
}

func TestBindLocksCodeToOneDevice(t *testing.T) {
	a := newTestApp(t)
	if _, err := a.records.generate(1, "", 0); err != nil {
		t.Fatal(err)
	}
	code := a.records.snapshot()[0].Code

	bind := func(udid string) map[string]any {
		body := `{"code":"` + code + `","udid":"` + udid + `"}`
		request := httptest.NewRequest(http.MethodPost, "/api/v1/bind", strings.NewReader(body))
		response := httptest.NewRecorder()
		a.handleBind(response, request)
		var out map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	if first := bind("device-one"); first["ok"] != true {
		t.Fatalf("first bind = %v, want ok", first)
	}
	// Same device again is idempotent.
	if again := bind("device-one"); again["ok"] != true {
		t.Fatalf("rebind same device = %v, want ok", again)
	}
	// A different device must be rejected.
	other := bind("device-two")
	if other["ok"] != false {
		t.Fatalf("bind other device = %v, want failure", other)
	}
	if other["message"] != "该授权码已绑定其他设备" {
		t.Fatalf("message = %v", other["message"])
	}
	if got := a.records.snapshot()[0].BoundUDID; got != "device-one" {
		t.Fatalf("bound udid = %q, want device-one", got)
	}
}

func TestBindRejectsUnknownCode(t *testing.T) {
	a := newTestApp(t)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/bind",
		strings.NewReader(`{"code":"AAAA-BBBB-CCCC-DDDD","udid":"device-one"}`))
	response := httptest.NewRecorder()

	a.handleBind(response, request)
	var out map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["ok"] != false || out["message"] != "授权码不存在" {
		t.Fatalf("result = %v, want 授权码不存在", out)
	}
}

func TestStatusReportsBindings(t *testing.T) {
	a := newTestApp(t)
	if _, err := a.records.generate(2, "", 0); err != nil {
		t.Fatal(err)
	}
	records := a.records.snapshot()
	bound, unbound := records[0].Code, records[1].Code
	if _, _, err := a.records.bind(bound, "device-one"); err != nil {
		t.Fatal(err)
	}

	url := "/api/v1/status?codes=" + bound + "," + unbound + ",ZZZZ-ZZZZ-ZZZZ-ZZZZ"
	response := httptest.NewRecorder()
	a.handleStatus(response, httptest.NewRequest(http.MethodGet, url, nil))

	var payload struct {
		Codes []struct {
			Code      string `json:"code"`
			Exists    bool   `json:"exists"`
			BoundUDID string `json:"boundUdid"`
		} `json:"codes"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Codes) != 3 {
		t.Fatalf("codes = %+v, want 3 entries", payload.Codes)
	}
	if !payload.Codes[0].Exists || payload.Codes[0].BoundUDID != "device-one" {
		t.Fatalf("bound entry = %+v", payload.Codes[0])
	}
	if !payload.Codes[1].Exists || payload.Codes[1].BoundUDID != "" {
		t.Fatalf("unbound entry = %+v", payload.Codes[1])
	}
	if payload.Codes[2].Exists {
		t.Fatalf("unknown code reported as existing: %+v", payload.Codes[2])
	}
}

func TestRecordStorePersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "records.json")
	store, err := newRecordStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.generate(2, "备注", 0); err != nil {
		t.Fatal(err)
	}
	code := store.snapshot()[0].Code
	if _, _, err := store.bind(code, "device-one"); err != nil {
		t.Fatal(err)
	}

	reloaded, err := newRecordStore(path)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, record := range reloaded.snapshot() {
		if record.Code == code {
			found = true
			if record.BoundUDID != "device-one" {
				t.Fatalf("bound udid after reload = %q", record.BoundUDID)
			}
		}
	}
	if !found {
		t.Fatal("record lost after reload")
	}
}

func TestRequireTokenWhenConfigured(t *testing.T) {
	a := newTestApp(t)
	a.apiToken = "secret-token"
	handler := a.requireToken(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	blocked := httptest.NewRecorder()
	handler(blocked, httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))
	if blocked.Code != http.StatusUnauthorized {
		t.Fatalf("without token status = %d, want 401", blocked.Code)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	request.Header.Set("X-License-Token", "secret-token")
	allowed := httptest.NewRecorder()
	handler(allowed, request)
	if allowed.Code != http.StatusOK {
		t.Fatalf("with token status = %d, want 200", allowed.Code)
	}
}

func TestRequireAuthRedirectsAnonymous(t *testing.T) {
	a := newTestApp(t)
	handler := a.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	anonymous := httptest.NewRecorder()
	handler(anonymous, httptest.NewRequest(http.MethodGet, "/admin/", nil))
	if anonymous.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", anonymous.Code)
	}

	token, err := a.sessions.create()
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	request.AddCookie(&http.Cookie{Name: "license_admin", Value: token})
	authed := httptest.NewRecorder()
	handler(authed, request)
	if authed.Code != http.StatusOK {
		t.Fatalf("authed status = %d, want 200", authed.Code)
	}
}

func TestBindReturnsVerifiableSignature(t *testing.T) {
	a := newTestApp(t)
	if _, err := a.records.generate(1, "", 0); err != nil {
		t.Fatal(err)
	}
	code := a.records.snapshot()[0].Code

	body := `{"code":"` + code + `","udid":"phone-one"}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/bind", strings.NewReader(body))
	response := httptest.NewRecorder()
	a.handleBind(response, request)

	var out struct {
		OK        bool   `json:"ok"`
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || out.Signature == "" {
		t.Fatalf("bind = %+v, want ok with a signature", out)
	}
	signature, err := hex.DecodeString(out.Signature)
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(a.signer.public, bindingMessage(code, "phone-one", nil), signature) {
		t.Fatal("signature does not verify against the app public key")
	}
}

func TestStatusIncludesSignatureForBoundCodes(t *testing.T) {
	a := newTestApp(t)
	if _, err := a.records.generate(1, "", 0); err != nil {
		t.Fatal(err)
	}
	code := a.records.snapshot()[0].Code
	if _, _, err := a.records.bind(code, "phone-one"); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	a.handleStatus(response, httptest.NewRequest(http.MethodGet, "/api/v1/status?codes="+code, nil))

	var payload struct {
		Codes []struct {
			Signature string `json:"signature"`
		} `json:"codes"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Codes) != 1 || payload.Codes[0].Signature == "" {
		t.Fatalf("status = %+v, want a signature", payload.Codes)
	}
}

func TestLoginLocksOutAfterRepeatedFailures(t *testing.T) {
	a := newTestApp(t)
	submit := func(password string) *httptest.ResponseRecorder {
		body := "username=admin&password=" + password + "&next=%2Fadmin%2F"
		request := httptest.NewRequest(http.MethodPost, "/admin/login/", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.RemoteAddr = "203.0.113.9:5555"
		response := httptest.NewRecorder()
		a.handleLoginSubmit(response, request)
		return response
	}

	for i := 0; i < 5; i++ {
		if location := submit("wrong").Header().Get("Location"); !strings.Contains(location, "error=1") {
			t.Fatalf("attempt %d: location = %q, want an error redirect", i+1, location)
		}
	}
	// The correct password must now be refused as well.
	location := submit("secret123").Header().Get("Location")
	if !strings.Contains(location, "locked=1") {
		t.Fatalf("after lockout location = %q, want locked=1", location)
	}
}

func TestIsLoopbackAddr(t *testing.T) {
	loopback := []string{"127.0.0.1:8090", "localhost:8090", "[::1]:8090"}
	for _, addr := range loopback {
		if !isLoopbackAddr(addr) {
			t.Fatalf("isLoopbackAddr(%q) = false, want true", addr)
		}
	}
	public := []string{"0.0.0.0:8090", ":8090", "82.157.209.182:8090"}
	for _, addr := range public {
		if isLoopbackAddr(addr) {
			t.Fatalf("isLoopbackAddr(%q) = true, want false", addr)
		}
	}
}

func TestUnbindReleasesCodeForAnotherDevice(t *testing.T) {
	a := newTestApp(t)
	if _, err := a.records.generate(1, "客户A", 0); err != nil {
		t.Fatal(err)
	}
	code := a.records.snapshot()[0].Code

	bind := func(udid string) map[string]any {
		body := `{"code":"` + code + `","udid":"` + udid + `"}`
		response := httptest.NewRecorder()
		a.handleBind(response, httptest.NewRequest(http.MethodPost, "/api/v1/bind", strings.NewReader(body)))
		var out map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	if out := bind("phone-one"); out["ok"] != true {
		t.Fatalf("first bind = %v", out)
	}
	if out := bind("phone-two"); out["ok"] != false {
		t.Fatalf("second device should be rejected, got %v", out)
	}

	unbindBody := `{"code":"` + code + `"}`
	response := httptest.NewRecorder()
	a.handleUnbind(response, httptest.NewRequest(http.MethodPost, "/admin/api/unbind", strings.NewReader(unbindBody)))
	if response.Code != http.StatusOK {
		t.Fatalf("unbind status = %d, body %s", response.Code, response.Body.String())
	}

	if out := bind("phone-two"); out["ok"] != true {
		t.Fatalf("after unbind the code should bind to a new device, got %v", out)
	}
	// The note must survive an unbind.
	if got := a.records.snapshot()[0].Note; got != "客户A" {
		t.Fatalf("note = %q, want 客户A", got)
	}
}

func TestUnbindRejectsUnknownCode(t *testing.T) {
	a := newTestApp(t)
	response := httptest.NewRecorder()
	a.handleUnbind(response, httptest.NewRequest(http.MethodPost, "/admin/api/unbind",
		strings.NewReader(`{"code":"AAAA-BBBB-CCCC-DDDD"}`)))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func sheetXMLFromXLSX(t *testing.T, data []byte) string {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("open xlsx: %v", err)
	}
	for _, file := range reader.File {
		if file.Name != "xl/worksheets/sheet1.xml" {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		body, err := io.ReadAll(rc)
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	t.Fatal("sheet1.xml not found")
	return ""
}

// Generating a batch must hand back an Excel file containing exactly that
// batch, so the operator can forward it to the customer right away.
func TestGenerateReturnsDownloadableBatch(t *testing.T) {
	a := newTestApp(t)
	if _, err := a.records.generate(1, "旧客户", 0); err != nil {
		t.Fatal(err)
	}
	oldCode := a.records.snapshot()[0].Code

	request := httptest.NewRequest(http.MethodPost, "/admin/api/generate", strings.NewReader(`{"count":2,"note":"客户A"}`))
	response := httptest.NewRecorder()
	a.handleGenerate(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", response.Code, response.Body.String())
	}
	var payload struct {
		Codes    []string `json:"codes"`
		Download string   `json:"download"`
		Filename string   `json:"filename"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Codes) != 2 {
		t.Fatalf("codes = %v, want 2", payload.Codes)
	}
	if payload.Download != "/admin/export.xlsx?batch=last" {
		t.Fatalf("download = %q", payload.Download)
	}
	if !strings.Contains(payload.Filename, "客户A") || !strings.HasSuffix(payload.Filename, ".xlsx") {
		t.Fatalf("filename = %q, want it to carry the customer note", payload.Filename)
	}

	download := httptest.NewRecorder()
	a.handleExportXLSX(download, httptest.NewRequest(http.MethodGet, "/admin/export.xlsx?batch=last", nil))
	if download.Code != http.StatusOK {
		t.Fatalf("download status = %d", download.Code)
	}
	disposition := download.Header().Get("Content-Disposition")
	if !strings.Contains(disposition, "filename*=UTF-8''") {
		t.Fatalf("Content-Disposition = %q, want a UTF-8 filename", disposition)
	}

	sheet := sheetXMLFromXLSX(t, download.Body.Bytes())
	if !strings.Contains(sheet, "授权码") {
		t.Fatal("sheet is missing the header row")
	}
	for _, code := range payload.Codes {
		if !strings.Contains(sheet, code) {
			t.Fatalf("batch download is missing %s", code)
		}
	}
	if strings.Contains(sheet, oldCode) {
		t.Fatalf("batch download leaked a code from a previous batch: %s", oldCode)
	}
}

func TestExportLastBatchWithoutGeneration(t *testing.T) {
	a := newTestApp(t)
	response := httptest.NewRecorder()
	a.handleExportXLSX(response, httptest.NewRequest(http.MethodGet, "/admin/export.xlsx?batch=last", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 before any batch exists", response.Code)
	}
}

func TestGenerateWithValidityPeriod(t *testing.T) {
	a := newTestApp(t)
	request := httptest.NewRequest(http.MethodPost, "/admin/api/generate",
		strings.NewReader(`{"count":2,"note":"包年客户","days":365}`))
	response := httptest.NewRecorder()
	a.handleGenerate(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", response.Code, response.Body.String())
	}

	records := a.records.snapshot()
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2", len(records))
	}
	want := time.Now().AddDate(0, 0, 365)
	for _, record := range records {
		if record.ExpiresAt == nil {
			t.Fatalf("record %s has no expiry", record.Code)
		}
		if diff := record.ExpiresAt.Sub(want); diff > time.Minute || diff < -time.Minute {
			t.Fatalf("expiry = %v, want about %v", record.ExpiresAt, want)
		}
	}
}

func TestGenerateWithoutDaysIsPermanent(t *testing.T) {
	a := newTestApp(t)
	request := httptest.NewRequest(http.MethodPost, "/admin/api/generate",
		strings.NewReader(`{"count":1,"note":"永久"}`))
	response := httptest.NewRecorder()
	a.handleGenerate(response, request)

	if record := a.records.snapshot()[0]; record.ExpiresAt != nil {
		t.Fatalf("expiry = %v, want nil for a permanent code", record.ExpiresAt)
	}
}

func TestGenerateRejectsNegativeDays(t *testing.T) {
	a := newTestApp(t)
	request := httptest.NewRequest(http.MethodPost, "/admin/api/generate",
		strings.NewReader(`{"count":1,"days":-5}`))
	response := httptest.NewRecorder()
	a.handleGenerate(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

func TestExpiredCodeCannotBind(t *testing.T) {
	a := newTestApp(t)
	if _, err := a.records.generate(1, "", 0); err != nil {
		t.Fatal(err)
	}
	// Backdate the expiry to simulate a code that ran out.
	code := a.records.snapshot()[0].Code
	past := time.Now().Add(-time.Hour)
	a.records.mu.Lock()
	a.records.records[0].ExpiresAt = &past
	a.records.mu.Unlock()

	body := `{"code":"` + code + `","udid":"phone-one"}`
	response := httptest.NewRecorder()
	a.handleBind(response, httptest.NewRequest(http.MethodPost, "/api/v1/bind", strings.NewReader(body)))

	var out map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["ok"] != false || out["message"] != "该授权码已过期" {
		t.Fatalf("result = %v, want 该授权码已过期", out)
	}
}

func TestStatusMarksExpiredCodeDisabled(t *testing.T) {
	a := newTestApp(t)
	if _, err := a.records.generate(1, "", 0); err != nil {
		t.Fatal(err)
	}
	code := a.records.snapshot()[0].Code
	if _, _, err := a.records.bind(code, "phone-one"); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	a.records.mu.Lock()
	a.records.records[0].ExpiresAt = &past
	a.records.mu.Unlock()

	response := httptest.NewRecorder()
	a.handleStatus(response, httptest.NewRequest(http.MethodGet, "/api/v1/status?codes="+code, nil))

	var payload struct {
		Codes []struct {
			Enabled   bool   `json:"enabled"`
			ExpiresAt string `json:"expiresAt"`
			Signature string `json:"signature"`
		} `json:"codes"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	item := payload.Codes[0]
	if item.Enabled {
		t.Fatal("an expired code must be reported as disabled")
	}
	if item.ExpiresAt == "" || item.Signature == "" {
		t.Fatalf("entry = %+v, want expiry and signature", item)
	}
}

func TestTableContainsExpiryColumn(t *testing.T) {
	a := newTestApp(t)
	if _, err := a.records.generate(1, "", 30); err != nil {
		t.Fatal(err)
	}
	rows := tableRows(a.records.snapshot())
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0][4] != "到期时间" {
		t.Fatalf("header = %v, want 到期时间 at index 4", rows[0])
	}
	if rows[1][4] == "" || rows[1][4] == "永久" {
		t.Fatalf("expiry cell = %q, want a timestamp", rows[1][4])
	}
	if rows[1][6] != "未绑定" {
		t.Fatalf("status = %q", rows[1][6])
	}
}

func TestSignatureCoversExpiry(t *testing.T) {
	a := newTestApp(t)
	if _, err := a.records.generate(1, "", 30); err != nil {
		t.Fatal(err)
	}
	record := a.records.snapshot()[0]

	signature := a.signer.signBinding(record.Code, "phone-one", record.ExpiresAt)
	raw, err := hex.DecodeString(signature)
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(a.signer.public, bindingMessage(record.Code, "phone-one", record.ExpiresAt), raw) {
		t.Fatal("signature must verify against the record expiry")
	}
	// A different expiry must not verify.
	other := record.ExpiresAt.AddDate(10, 0, 0)
	if ed25519.Verify(a.signer.public, bindingMessage(record.Code, "phone-one", &other), raw) {
		t.Fatal("signature must not verify for a different expiry")
	}
}
