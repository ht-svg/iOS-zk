package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func writeTestXLSX(t *testing.T, path string, rows []string) {
	t.Helper()
	var sheet strings.Builder
	sheet.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	for i, value := range rows {
		ref := "A" + itoa(i+1)
		sheet.WriteString(`<row r="` + itoa(i+1) + `"><c r="` + ref + `" t="inlineStr"><is><t>` + value + `</t></is></c></row>`)
	}
	sheet.WriteString(`</sheetData></worksheet>`)

	var buf bytes.Buffer
	archive := zip.NewWriter(&buf)
	parts := map[string]string{
		"[Content_Types].xml":        `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>`,
		"_rels/.rels":                `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`,
		"xl/workbook.xml":            `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="授权码" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   sheet.String(),
	}
	for name, body := range parts {
		writer, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func TestReadLicenseCodesFromXLSX(t *testing.T) {
	path := filepath.Join(t.TempDir(), "授权码.xlsx")
	writeTestXLSX(t, path, []string{"授权码", "ABCD-EFGH-JKLM-NPQR", "WXYZ-2345-6789-ABCD"})

	codes, err := readLicenseCodes(path)
	if err != nil {
		t.Fatalf("readLicenseCodes: %v", err)
	}
	want := []string{"授权码", "ABCD-EFGH-JKLM-NPQR", "WXYZ-2345-6789-ABCD"}
	if len(codes) != len(want) {
		t.Fatalf("codes = %v, want %v", codes, want)
	}
	for i := range want {
		if codes[i] != want[i] {
			t.Fatalf("codes[%d] = %q, want %q", i, codes[i], want[i])
		}
	}
}

func TestReadLicenseCodesMissingFile(t *testing.T) {
	if _, err := readLicenseCodes(filepath.Join(t.TempDir(), "nope.xlsx")); err == nil {
		t.Fatal("reading a missing table should fail")
	}
}

func TestNormalizeLicenseCode(t *testing.T) {
	want := "ABCD-EFGH-JKLM-NPQR"
	for _, input := range []string{"ABCD-EFGH-JKLM-NPQR", "abcdefghjklmnpqr", "abcd efgh jklm npqr"} {
		if got := normalizeLicenseCode(input); got != want {
			t.Fatalf("normalizeLicenseCode(%q) = %q, want %q", input, got, want)
		}
	}
	if got := normalizeLicenseCode("授权码"); got != "" {
		t.Fatalf("header row should not normalize, got %q", got)
	}
}

// useTestSigningKey installs a throwaway keypair as the trusted public key and
// returns the private half so the fake server can sign like the real backend.
func useTestSigningKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	previous := licensePublicKeyHex
	licensePublicKeyHex = hex.EncodeToString(public)
	t.Cleanup(func() { licensePublicKeyHex = previous })
	return private
}

func testSignature(private ed25519.PrivateKey, code, udid, expiresAt string) string {
	expiry, err := normalizeExpiry(expiresAt)
	if err != nil {
		panic(err)
	}
	return hex.EncodeToString(ed25519.Sign(private, bindingMessage(code, udid, expiry)))
}

type licenseFixture struct {
	server *server
	backup string
}

// authorizeDevices marks phones as licensed. Device-operation tests need this
// because the handlers refuse to drive unlicensed hardware.
func authorizeDevices(s *server, udids ...string) {
	states := make(map[string]licenseDeviceState, len(udids))
	for _, udid := range udids {
		states[udid] = licenseDeviceState{Authorized: true, Code: "TEST-CODE", Source: "server"}
	}
	s.license = licenseStatus{Devices: states}
}

func newLicenseFixture(t *testing.T, codes []string, serverURL string, devices ...string) licenseFixture {
	t.Helper()
	dir := t.TempDir()
	table := filepath.Join(dir, "授权码.xlsx")
	writeTestXLSX(t, table, append([]string{"授权码"}, codes...))
	backup := filepath.Join(dir, "授权备份.json")

	runtimeCfg.LicenseTablePath = table
	runtimeCfg.LicenseBackupPath = backup
	t.Cleanup(func() {
		runtimeCfg.LicenseTablePath = ""
		runtimeCfg.LicenseBackupPath = ""
	})

	return licenseFixture{
		server: &server{
			config:       Config{LicenseServer: serverURL},
			deviceLister: func() []string { return devices },
		},
		backup: backup,
	}
}

// fakeLicenseServer serves /api/v1/status and /api/v1/bind backed by maps and
// signs bindings the same way the real backend does. expiry maps a code to its
// RFC3339 expiry; a missing entry means the code never expires.
func fakeLicenseServer(t *testing.T, bound map[string]string, expiry map[string]string, private ed25519.PrivateKey) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/status":
			mu.Lock()
			items := make([]map[string]any, 0)
			for _, code := range strings.Split(r.URL.Query().Get("codes"), ",") {
				normalized := normalizeLicenseCode(code)
				if normalized == "" {
					continue
				}
				expires := expiry[normalized]
				enabled := !bindingExpired(expires)
				entry := map[string]any{
					"code": normalized, "exists": true, "enabled": enabled, "boundUdid": bound[normalized],
				}
				if expires != "" {
					entry["expiresAt"] = expires
				}
				if udid := bound[normalized]; udid != "" {
					entry["signature"] = testSignature(private, normalized, udid, expires)
				}
				items = append(items, entry)
			}
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"codes": items})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/bind":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			code := normalizeLicenseCode(body["code"])
			mu.Lock()
			current := bound[code]
			if current != "" && current != body["udid"] {
				mu.Unlock()
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "message": "该授权码已绑定其他设备"})
				return
			}
			bound[code] = body["udid"]
			mu.Unlock()
			expires := expiry[code]
			payload := map[string]any{
				"ok": true, "code": code, "udid": body["udid"],
				"signature": testSignature(private, code, body["udid"], expires),
			}
			if expires != "" {
				payload["expiresAt"] = expires
			}
			_ = json.NewEncoder(w).Encode(payload)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestRefreshLicenseBindsFreeCodeToDevice(t *testing.T) {
	const code = "ABCD-EFGH-JKLM-NPQR"
	private := useTestSigningKey(t)
	bound := map[string]string{}
	fake := fakeLicenseServer(t, bound, nil, private)
	defer fake.Close()

	fixture := newLicenseFixture(t, []string{code}, fake.URL, "phone-one")
	status := fixture.server.refreshLicense(context.Background())

	state := status.Devices["phone-one"]
	if !state.Authorized || state.Code != code || state.Source != "server" {
		t.Fatalf("device state = %+v, want authorized via server", state)
	}
	if bound[code] != "phone-one" {
		t.Fatalf("server bound %q to %q, want phone-one", code, bound[code])
	}
	saved := loadLicenseBackup(fixture.backup)
	entry, ok := saved.entryFor("phone-one")
	if !ok || entry.Code != code {
		t.Fatalf("backup = %+v, want phone-one -> %s", saved, code)
	}
	if !verifyBindingSignature(entry.Code, entry.UDID, entry.ExpiresAt, entry.Signature) {
		t.Fatal("the stored backup signature must verify")
	}
}

func TestRefreshLicenseStoresExpiryInBackup(t *testing.T) {
	const code = "ABCD-EFGH-JKLM-NPQR"
	expires := time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339)
	private := useTestSigningKey(t)
	bound := map[string]string{}
	fake := fakeLicenseServer(t, bound, map[string]string{code: expires}, private)
	defer fake.Close()

	fixture := newLicenseFixture(t, []string{code}, fake.URL, "phone-one")
	status := fixture.server.refreshLicense(context.Background())

	state := status.Devices["phone-one"]
	if !state.Authorized || state.ExpiresAt != expires {
		t.Fatalf("device state = %+v, want authorized with expiry %s", state, expires)
	}
	entry, ok := loadLicenseBackup(fixture.backup).entryFor("phone-one")
	if !ok || entry.ExpiresAt != expires {
		t.Fatalf("backup entry = %+v, want expiresAt %s", entry, expires)
	}
}

func TestRefreshLicenseRejectsExpiredCodeOnline(t *testing.T) {
	const code = "ABCD-EFGH-JKLM-NPQR"
	expires := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	private := useTestSigningKey(t)
	bound := map[string]string{code: "phone-one"}
	fake := fakeLicenseServer(t, bound, map[string]string{code: expires}, private)
	defer fake.Close()

	fixture := newLicenseFixture(t, []string{code}, fake.URL, "phone-one")
	status := fixture.server.refreshLicense(context.Background())

	state := status.Devices["phone-one"]
	if state.Authorized {
		t.Fatalf("device state = %+v, want unauthorized after expiry", state)
	}
	if !strings.Contains(state.Reason, "过期") {
		t.Fatalf("reason = %q, want an expiry hint", state.Reason)
	}
}

func TestRefreshLicenseOfflineRejectsExpiredBackup(t *testing.T) {
	const code = "ABCD-EFGH-JKLM-NPQR"
	expires := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	private := useTestSigningKey(t)
	fake := fakeLicenseServer(t, map[string]string{}, nil, private)
	url := fake.URL
	fake.Close()

	fixture := newLicenseFixture(t, []string{code}, url, "phone-one")
	backup := licenseBackup{}
	// Properly signed, but the signed expiry is in the past.
	backup.remember("phone-one", code, testSignature(private, code, "phone-one", expires), expires)
	if err := saveLicenseBackup(fixture.backup, backup); err != nil {
		t.Fatal(err)
	}

	status := fixture.server.refreshLicense(context.Background())
	state := status.Devices["phone-one"]
	if state.Authorized {
		t.Fatalf("expired backup was accepted: %+v", state)
	}
	if !strings.Contains(state.Reason, "过期") {
		t.Fatalf("reason = %q, want an expiry hint", state.Reason)
	}
}

// Extending the expiry in the backup must break the signature.
func TestRefreshLicenseOfflineRejectsTamperedExpiry(t *testing.T) {
	const code = "ABCD-EFGH-JKLM-NPQR"
	original := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	extended := time.Now().Add(3650 * 24 * time.Hour).UTC().Format(time.RFC3339)
	private := useTestSigningKey(t)
	fake := fakeLicenseServer(t, map[string]string{}, nil, private)
	url := fake.URL
	fake.Close()

	fixture := newLicenseFixture(t, []string{code}, url, "phone-one")
	backup := licenseBackup{}
	backup.remember("phone-one", code, testSignature(private, code, "phone-one", original), extended)
	if err := saveLicenseBackup(fixture.backup, backup); err != nil {
		t.Fatal(err)
	}

	status := fixture.server.refreshLicense(context.Background())
	if status.Devices["phone-one"].Authorized {
		t.Fatal("a tampered expiry must fail signature verification")
	}
}

func TestRefreshLicenseReusesExistingBinding(t *testing.T) {
	const code = "ABCD-EFGH-JKLM-NPQR"
	private := useTestSigningKey(t)
	bound := map[string]string{code: "phone-one"}
	fake := fakeLicenseServer(t, bound, nil, private)
	defer fake.Close()

	fixture := newLicenseFixture(t, []string{code}, fake.URL, "phone-one")
	backup := licenseBackup{}
	backup.remember("phone-one", code, testSignature(private, code, "phone-one", ""), "")
	if err := saveLicenseBackup(fixture.backup, backup); err != nil {
		t.Fatal(err)
	}

	status := fixture.server.refreshLicense(context.Background())
	if state := status.Devices["phone-one"]; !state.Authorized || state.Code != code {
		t.Fatalf("device state = %+v, want authorized", state)
	}
}

func TestRefreshLicenseRejectsCodeBoundElsewhere(t *testing.T) {
	const code = "ABCD-EFGH-JKLM-NPQR"
	private := useTestSigningKey(t)
	bound := map[string]string{code: "other-phone"}
	fake := fakeLicenseServer(t, bound, nil, private)
	defer fake.Close()

	fixture := newLicenseFixture(t, []string{code}, fake.URL, "phone-one")
	status := fixture.server.refreshLicense(context.Background())

	state := status.Devices["phone-one"]
	if state.Authorized {
		t.Fatalf("device state = %+v, want unauthorized", state)
	}
	if !strings.Contains(state.Reason, "没有可用") {
		t.Fatalf("reason = %q, want a hint about no free code", state.Reason)
	}
}

func TestRefreshLicenseOfflineUsesBackup(t *testing.T) {
	const code = "ABCD-EFGH-JKLM-NPQR"
	private := useTestSigningKey(t)
	fake := fakeLicenseServer(t, map[string]string{}, nil, private)
	url := fake.URL
	fake.Close() // unreachable from now on

	fixture := newLicenseFixture(t, []string{code}, url, "phone-one")
	backup := licenseBackup{}
	backup.remember("phone-one", code, testSignature(private, code, "phone-one", ""), "")
	if err := saveLicenseBackup(fixture.backup, backup); err != nil {
		t.Fatal(err)
	}

	status := fixture.server.refreshLicense(context.Background())
	if !status.Offline {
		t.Fatalf("status = %+v, want offline", status)
	}
	state := status.Devices["phone-one"]
	if !state.Authorized || state.Source != "backup" {
		t.Fatalf("device state = %+v, want authorized via backup", state)
	}
}

// This is the regression test for the forged-backup hole: a hand-written
// backup without a backend signature must never authorise a device.
func TestRefreshLicenseOfflineRejectsForgedBackup(t *testing.T) {
	private := useTestSigningKey(t)
	fake := fakeLicenseServer(t, map[string]string{}, nil, private)
	url := fake.URL
	fake.Close()

	fixture := newLicenseFixture(t, []string{"FAKE-AAAA-BBBB-CCCC"}, url, "phone-one")
	backup := licenseBackup{}
	backup.remember("phone-one", "FAKE-AAAA-BBBB-CCCC", "", "") // no signature
	if err := saveLicenseBackup(fixture.backup, backup); err != nil {
		t.Fatal(err)
	}

	status := fixture.server.refreshLicense(context.Background())
	state := status.Devices["phone-one"]
	if state.Authorized {
		t.Fatalf("forged backup was accepted: %+v", state)
	}
	if !strings.Contains(state.Reason, "签名") {
		t.Fatalf("reason = %q, want a signature failure", state.Reason)
	}
}

func TestRefreshLicenseOfflineRejectsWrongSignature(t *testing.T) {
	trusted := useTestSigningKey(t) // trusted public key installed
	_, untrusted, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	fake := fakeLicenseServer(t, map[string]string{}, nil, trusted)
	url := fake.URL
	fake.Close()

	const code = "ABCD-EFGH-JKLM-NPQR"
	fixture := newLicenseFixture(t, []string{code}, url, "phone-one")
	backup := licenseBackup{}
	// Signed with a different key than the one the client trusts.
	backup.remember("phone-one", code, testSignature(untrusted, code, "phone-one", ""), "")
	if err := saveLicenseBackup(fixture.backup, backup); err != nil {
		t.Fatal(err)
	}

	status := fixture.server.refreshLicense(context.Background())
	if status.Devices["phone-one"].Authorized {
		t.Fatal("a signature from the wrong key must not authorise")
	}
}

func TestRefreshLicenseOfflineRejectsCodeRemovedFromTable(t *testing.T) {
	private := useTestSigningKey(t)
	fake := fakeLicenseServer(t, map[string]string{}, nil, private)
	url := fake.URL
	fake.Close()

	// The table no longer contains the code that the backup remembers.
	fixture := newLicenseFixture(t, []string{"ZZZZ-ZZZZ-ZZZZ-ZZZZ"}, url, "phone-one")
	backup := licenseBackup{}
	backup.remember("phone-one", "ABCD-EFGH-JKLM-NPQR", testSignature(private, "ABCD-EFGH-JKLM-NPQR", "phone-one", ""), "")
	if err := saveLicenseBackup(fixture.backup, backup); err != nil {
		t.Fatal(err)
	}

	status := fixture.server.refreshLicense(context.Background())
	state := status.Devices["phone-one"]
	if state.Authorized {
		t.Fatalf("device state = %+v, want unauthorized when the code left the table", state)
	}
	if !strings.Contains(state.Reason, "不在授权码表格") {
		t.Fatalf("reason = %q, want a hint about the missing code", state.Reason)
	}
}

func TestRefreshLicenseOfflineWithoutBackupBlocks(t *testing.T) {
	fixture := newLicenseFixture(t, []string{"ABCD-EFGH-JKLM-NPQR"}, "", "phone-one")
	status := fixture.server.refreshLicense(context.Background())

	state := status.Devices["phone-one"]
	if state.Authorized {
		t.Fatalf("device state = %+v, want unauthorized without a backup", state)
	}
	if !strings.Contains(state.Reason, "本地备份") {
		t.Fatalf("reason = %q, want a hint about the missing backup", state.Reason)
	}
}

func TestRefreshLicenseMissingTableBlocks(t *testing.T) {
	runtimeCfg.LicenseTablePath = filepath.Join(t.TempDir(), "missing.xlsx")
	runtimeCfg.LicenseBackupPath = filepath.Join(t.TempDir(), "backup.json")
	t.Cleanup(func() {
		runtimeCfg.LicenseTablePath = ""
		runtimeCfg.LicenseBackupPath = ""
	})

	s := &server{config: Config{}, deviceLister: func() []string { return []string{"phone-one"} }}
	status := s.refreshLicense(context.Background())
	if status.Devices["phone-one"].Authorized {
		t.Fatalf("status = %+v, want unauthorized without a table", status)
	}
}

// A remote server over plain HTTP could be impersonated, so it is refused
// outright instead of being treated as "offline".
func TestRefreshLicenseRejectsInsecureServerURL(t *testing.T) {
	fixture := newLicenseFixture(t, []string{"ABCD-EFGH-JKLM-NPQR"}, "http://license.example.com:8090", "phone-one")
	status := fixture.server.refreshLicense(context.Background())
	if status.Devices["phone-one"].Authorized {
		t.Fatalf("status = %+v, want unauthorized for plain HTTP", status)
	}
	if !strings.Contains(status.Reason, "https") {
		t.Fatalf("reason = %q, want an https hint", status.Reason)
	}
}

func TestLoopbackServerAllowsPlainHTTP(t *testing.T) {
	fixture := newLicenseFixture(t, []string{"ABCD-EFGH-JKLM-NPQR"}, "http://127.0.0.1:8090", "phone-one")
	if _, err := fixture.server.licenseServerEndpoint(); err != nil {
		t.Fatalf("loopback http should be allowed: %v", err)
	}
}

// A shipped client must reach the cloud backend without any local config.
func TestDefaultLicenseServerIsUsedWhenUnconfigured(t *testing.T) {
	previous := defaultLicenseServer
	defaultLicenseServer = "https://license.example.com"
	t.Cleanup(func() { defaultLicenseServer = previous })

	fixture := newLicenseFixture(t, []string{"ABCD-EFGH-JKLM-NPQR"}, "", "phone-one")
	endpoint, err := fixture.server.licenseServerEndpoint()
	if err != nil {
		t.Fatalf("endpoint: %v", err)
	}
	if endpoint != "https://license.example.com" {
		t.Fatalf("endpoint = %q, want the baked-in default", endpoint)
	}
}

func TestConfiguredServerOverridesDefault(t *testing.T) {
	previous := defaultLicenseServer
	defaultLicenseServer = "https://default.example.com"
	t.Cleanup(func() { defaultLicenseServer = previous })

	fixture := newLicenseFixture(t, []string{"ABCD-EFGH-JKLM-NPQR"}, "https://custom.example.com", "phone-one")
	endpoint, err := fixture.server.licenseServerEndpoint()
	if err != nil {
		t.Fatalf("endpoint: %v", err)
	}
	if endpoint != "https://custom.example.com" {
		t.Fatalf("endpoint = %q, want the configured value to win", endpoint)
	}
}

func TestLicenseBackupRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backup.json")
	backup := licenseBackup{}
	backup.remember("phone-one", "ABCD-EFGH-JKLM-NPQR", "sig-one", "2027-01-01T00:00:00Z")
	backup.remember("phone-two", "WXYZ-2345-6789-ABCD", "sig-two", "")
	backup.remember("phone-one", "ABCD-EFGH-JKLM-NPQR", "sig-one", "2027-01-01T00:00:00Z") // idempotent

	if err := saveLicenseBackup(path, backup); err != nil {
		t.Fatal(err)
	}
	loaded := loadLicenseBackup(path)
	if len(loaded.Entries) != 2 {
		t.Fatalf("entries = %+v, want 2", loaded.Entries)
	}
	entry, ok := loaded.entryFor("phone-two")
	if !ok || entry.Code != "WXYZ-2345-6789-ABCD" || entry.Signature != "sig-two" || entry.ExpiresAt != "" {
		t.Fatalf("phone-two -> %+v, want the stored code and signature", entry)
	}
	first, _ := loaded.entryFor("phone-one")
	if first.ExpiresAt != "2027-01-01T00:00:00Z" {
		t.Fatalf("expiry = %q, want it persisted", first.ExpiresAt)
	}
}

func TestBindingExpired(t *testing.T) {
	if bindingExpired("") {
		t.Fatal("an empty expiry means permanent")
	}
	if !bindingExpired(time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)) {
		t.Fatal("a past expiry should be expired")
	}
	if bindingExpired(time.Now().Add(time.Hour).UTC().Format(time.RFC3339)) {
		t.Fatal("a future expiry should be valid")
	}
	if !bindingExpired("not-a-date") {
		t.Fatal("an unparsable expiry must be treated as expired")
	}
}

func TestRequireDeviceLicenseBlocksUnlicensed(t *testing.T) {
	s := &server{}
	s.setLicense(licenseStatus{Devices: map[string]licenseDeviceState{
		"phone-one": {Authorized: true, Code: "ABCD-EFGH-JKLM-NPQR"},
		"phone-two": {Authorized: false},
	}})

	allowed := httptest.NewRecorder()
	if !s.requireDeviceLicense(allowed, "phone-one") {
		t.Fatal("licensed device should be allowed")
	}

	blocked := httptest.NewRecorder()
	if s.requireDeviceLicense(blocked, "phone-two") {
		t.Fatal("unlicensed device should be blocked")
	}
	if blocked.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", blocked.Code)
	}
	if !strings.Contains(blocked.Body.String(), "unauthorized") {
		t.Fatalf("body = %s, want the unauthorized code", blocked.Body.String())
	}
}

// The server is authoritative: losing the local backup must not lock a phone
// out, because the binding still exists on the backend.
func TestRefreshLicenseRecoversWhenBackupLost(t *testing.T) {
	const code = "ABCD-EFGH-JKLM-NPQR"
	private := useTestSigningKey(t)
	bound := map[string]string{code: "phone-one"}
	fake := fakeLicenseServer(t, bound, nil, private)
	defer fake.Close()

	fixture := newLicenseFixture(t, []string{code}, fake.URL, "phone-one")
	// Deliberately no 授权备份.json on disk.
	status := fixture.server.refreshLicense(context.Background())

	state := status.Devices["phone-one"]
	if !state.Authorized || state.Code != code {
		t.Fatalf("device state = %+v, want the server binding to be adopted", state)
	}
	entry, ok := loadLicenseBackup(fixture.backup).entryFor("phone-one")
	if !ok || entry.Code != code || entry.Signature == "" {
		t.Fatalf("backup entry = %+v, want it rebuilt from the server", entry)
	}
}

// When a phone holds both an expired and a valid binding, the valid one must
// win — otherwise a renewal would still leave the customer locked out.
func TestRefreshLicensePrefersValidBindingOverExpired(t *testing.T) {
	const expiredCode = "AAAA-BBBB-CCCC-DDDD"
	const validCode = "ZZZZ-YYYY-XXXX-WWWW"
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	future := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)

	private := useTestSigningKey(t)
	bound := map[string]string{expiredCode: "phone-one", validCode: "phone-one"}
	fake := fakeLicenseServer(t, bound, map[string]string{expiredCode: past, validCode: future}, private)
	defer fake.Close()

	fixture := newLicenseFixture(t, []string{expiredCode, validCode}, fake.URL, "phone-one")
	status := fixture.server.refreshLicense(context.Background())

	state := status.Devices["phone-one"]
	if !state.Authorized || state.Code != validCode {
		t.Fatalf("device state = %+v, want the valid binding to win", state)
	}
}
