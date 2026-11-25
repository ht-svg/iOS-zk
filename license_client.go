package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Licensing is per phone: one code authorises exactly one device. On every
// start the client scans 授权码.xlsx, reconciles it with the server's bindings
// and keeps a local backup of the pairs it learned. When the server cannot be
// reached the backup is used instead — but only signatures made by the backend
// private key are accepted, so hand-editing the files no longer helps.
const (
	licenseTableName  = "授权码.xlsx"
	licenseBackupName = "授权备份.json"
)

// licensePublicKeyHex is the Ed25519 public key of the license backend. It is
// compiled into the client on purpose: the private key never leaves the server,
// so nobody can mint a valid binding signature locally.
// Regenerate with: license-admin -key signing.key  (and update this value).
var licensePublicKeyHex = "0f3c7e9a1b3af12df35b1c16d359acec5b0d4ba37e1879f0142b71eaf0a53e63"

// bindingMessage must match bindingMessage() in license-admin/signing.go.
// The expiry is signed too, so a customer cannot extend it by editing files.
func bindingMessage(code, udid, expiry string) []byte {
	return []byte(strings.ToUpper(strings.TrimSpace(code)) + "|" + strings.TrimSpace(udid) + "|" + expiry)
}

// normalizeExpiry canonicalises the expiry the backend sent (RFC3339, UTC).
func normalizeExpiry(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return "", err
	}
	return parsed.UTC().Format(time.RFC3339), nil
}

func verifyBindingSignature(code, udid, expiresAt, signatureHex string) bool {
	publicKey, err := hex.DecodeString(licensePublicKeyHex)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return false
	}
	signature, err := hex.DecodeString(strings.TrimSpace(signatureHex))
	if err != nil || len(signature) != ed25519.SignatureSize {
		return false
	}
	expiry, err := normalizeExpiry(expiresAt)
	if err != nil {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(publicKey), bindingMessage(code, udid, expiry), signature)
}

// bindingExpired reports whether the signed expiry has passed. An unparsable
// value is treated as expired rather than as "permanent".
func bindingExpired(expiresAt string) bool {
	raw := strings.TrimSpace(expiresAt)
	if raw == "" {
		return false
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return true
	}
	return time.Now().After(parsed)
}

type licenseDeviceState struct {
	Authorized bool   `json:"authorized"`
	Code       string `json:"code,omitempty"`
	Source     string `json:"source,omitempty"` // server | backup
	ExpiresAt  string `json:"expiresAt,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

type licenseStatus struct {
	Offline     bool                          `json:"offline"`
	Reason      string                        `json:"reason"`
	Server      string                        `json:"server,omitempty"`
	TablePath   string                        `json:"tablePath"`
	BackupPath  string                        `json:"backupPath"`
	CodeCount   int                           `json:"codeCount"`
	DeviceCount int                           `json:"deviceCount"`
	CheckedAt   string                        `json:"checkedAt,omitempty"`
	Devices     map[string]licenseDeviceState `json:"devices"`
}

// licenseHTTPClient is separate from the Appium client: the license server is
// usually remote, and the Appium client deliberately refuses non-local URLs.
var licenseHTTPClient = &http.Client{Timeout: 8 * time.Second}

func executableDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

func (s *server) licenseTablePath() string {
	if path := runtimeCfg.LicenseTablePath; path != "" {
		return path
	}
	return filepath.Join(executableDir(), licenseTableName)
}

func (s *server) licenseBackupPath() string {
	if path := runtimeCfg.LicenseBackupPath; path != "" {
		return path
	}
	return filepath.Join(executableDir(), licenseBackupName)
}

func (s *server) licenseServerURL() string {
	serverURL, _ := s.licenseServerEndpoint()
	return serverURL
}

// defaultLicenseServer / defaultLicenseToken are baked in at build time so a
// shipped client reaches the cloud backend without a config file:
//
//	go build -ldflags "-X main.defaultLicenseServer=https://license.example.com \
//	                   -X main.defaultLicenseToken=xxxx"
//
// A value in config.json or the matching env var still wins.
var (
	defaultLicenseServer = ""
	defaultLicenseToken  = ""
)

// licenseServerEndpoint returns the configured URL, rejecting plain HTTP for
// anything that is not this machine so bindings cannot be forged by a MITM.
func (s *server) licenseServerEndpoint() (string, error) {
	s.mu.RLock()
	configured := s.config.LicenseServer
	s.mu.RUnlock()
	if configured == "" {
		configured = os.Getenv(runtimeEnvPrefix + "LICENSE_SERVER")
	}
	if configured == "" {
		configured = defaultLicenseServer
	}
	trimmed := strings.TrimRight(strings.TrimSpace(configured), "/")
	if trimmed == "" {
		return "", nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("授权服务器地址无效：%s", trimmed)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		return trimmed, nil
	case "http":
		if isLoopbackHost(parsed.Hostname()) {
			return trimmed, nil
		}
		return "", errors.New("授权服务器地址必须使用 https://（本机地址除外）")
	default:
		return "", fmt.Errorf("授权服务器地址必须使用 https://：%s", trimmed)
	}
}

func isLoopbackHost(host string) bool {
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *server) licenseAPIToken() string {
	s.mu.RLock()
	token := s.config.LicenseToken
	s.mu.RUnlock()
	if token == "" {
		token = os.Getenv(runtimeEnvPrefix + "LICENSE_TOKEN")
	}
	if token == "" {
		token = defaultLicenseToken
	}
	return strings.TrimSpace(token)
}

func (s *server) setLicense(status licenseStatus) {
	if status.Devices == nil {
		status.Devices = make(map[string]licenseDeviceState)
	}
	s.mu.Lock()
	s.license = status
	s.mu.Unlock()
}

func (s *server) currentLicense() licenseStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.license.Devices == nil {
		s.license.Devices = make(map[string]licenseDeviceState)
	}
	return s.license
}

// deviceAuthorized reports whether the given phone holds a valid binding.
func (s *server) deviceAuthorized(udid string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, ok := s.license.Devices[udid]
	return ok && state.Authorized
}

// requireDeviceLicense blocks device operations for unlicensed phones. The UI
// already masks the screen, but the API must not rely on the browser.
func (s *server) requireDeviceLicense(w http.ResponseWriter, udid string) bool {
	if s.deviceAuthorized(udid) {
		return true
	}
	writeErrorWithCode(w, http.StatusForbidden, CodeUnauthorized, errDeviceUnlicensed)
	return false
}

// --- local backup -----------------------------------------------------------

type licenseBackupEntry struct {
	UDID      string `json:"udid"`
	Code      string `json:"code"`
	Signature string `json:"signature,omitempty"`
	ExpiresAt string `json:"expiresAt,omitempty"`
	BoundAt   string `json:"boundAt,omitempty"`
}

type licenseBackup struct {
	UpdatedAt string               `json:"updatedAt"`
	Server    string               `json:"server,omitempty"`
	Entries   []licenseBackupEntry `json:"entries"`
}

func loadLicenseBackup(path string) licenseBackup {
	backup := licenseBackup{}
	data, err := os.ReadFile(path)
	if err != nil {
		return backup
	}
	_ = json.Unmarshal(data, &backup)
	return backup
}

func (b licenseBackup) entryFor(udid string) (licenseBackupEntry, bool) {
	for _, entry := range b.Entries {
		if entry.UDID == udid && entry.Code != "" {
			return entry, true
		}
	}
	return licenseBackupEntry{}, false
}

func (b licenseBackup) codeFor(udid string) (string, bool) {
	entry, ok := b.entryFor(udid)
	return entry.Code, ok
}

// remember records or replaces the binding for a device, including the
// backend signature and expiry that make it verifiable offline.
func (b *licenseBackup) remember(udid, code, signature, expiresAt string) {
	if udid == "" || code == "" {
		return
	}
	for i := range b.Entries {
		if b.Entries[i].UDID == udid {
			if b.Entries[i].Code == code && b.Entries[i].Signature == signature && b.Entries[i].ExpiresAt == expiresAt {
				return
			}
			b.Entries[i].Code = code
			b.Entries[i].Signature = signature
			b.Entries[i].ExpiresAt = expiresAt
			b.Entries[i].BoundAt = time.Now().Format("2006-01-02 15:04:05")
			return
		}
	}
	b.Entries = append(b.Entries, licenseBackupEntry{
		UDID:      udid,
		Code:      code,
		Signature: signature,
		ExpiresAt: expiresAt,
		BoundAt:   time.Now().Format("2006-01-02 15:04:05"),
	})
}

func saveLicenseBackup(path string, backup licenseBackup) error {
	backup.UpdatedAt = time.Now().Format("2006-01-02 15:04:05")
	data, err := json.MarshalIndent(backup, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// --- reconciliation ---------------------------------------------------------

// connectedUDIDs lists the phones currently attached. A licence error must not
// take the app down, so failures simply yield an empty list.
func (s *server) connectedUDIDs() []string {
	found, err := listUSBMuxDevices(runtimeCfg.USBMuxAddress)
	if err != nil {
		return nil
	}
	udids := make([]string, 0, len(found))
	for _, device := range found {
		udids = append(udids, device.UDID)
	}
	return udids
}

// listLicenseDevices returns the phones to reconcile. Tests replace deviceLister
// so they do not need a real usbmuxd.
func (s *server) listLicenseDevices() []string {
	if s.deviceLister != nil {
		return s.deviceLister()
	}
	return s.connectedUDIDs()
}

func (s *server) refreshLicense(ctx context.Context) licenseStatus {
	status := licenseStatus{
		TablePath:  s.licenseTablePath(),
		BackupPath: s.licenseBackupPath(),
		Devices:    make(map[string]licenseDeviceState),
	}
	defer func() {
		status.CheckedAt = time.Now().Format("2006-01-02 15:04:05")
		s.setLicense(status)
	}()

	raw, err := readLicenseCodes(status.TablePath)
	if err != nil {
		status.Reason = "未找到或无法读取授权码表格：" + err.Error()
		return status
	}
	codes := make([]string, 0, len(raw))
	inTable := make(map[string]bool, len(raw))
	for _, value := range raw {
		code := normalizeLicenseCode(value)
		if code == "" || inTable[code] {
			continue
		}
		inTable[code] = true
		codes = append(codes, code)
	}
	status.CodeCount = len(codes)

	devices := s.listLicenseDevices()
	status.DeviceCount = len(devices)
	if len(codes) == 0 {
		status.Reason = "授权码表格中没有有效的授权码"
		return status
	}

	backup := loadLicenseBackup(status.BackupPath)
	serverURL, endpointErr := s.licenseServerEndpoint()
	status.Server = serverURL
	if endpointErr != nil {
		// An insecure or malformed address is a configuration error, not an
		// offline situation: refuse to trust anything until it is fixed.
		status.Reason = endpointErr.Error()
		for _, udid := range devices {
			status.Devices[udid] = licenseDeviceState{Reason: endpointErr.Error()}
		}
		return status
	}

	if serverURL != "" {
		bindings, err := s.queryLicenseBindings(ctx, serverURL, codes)
		if err == nil {
			s.reconcileWithServer(ctx, status.Devices, &backup, devices, bindings, serverURL)
			backup.Server = serverURL
			if err := saveLicenseBackup(status.BackupPath, backup); err != nil {
				status.Reason = "绑定成功，但写入本地备份失败：" + err.Error()
				return status
			}
			status.Reason = summarizeDevices(status.Devices, len(devices))
			return status
		}
		status.Offline = true
		status.Reason = "无法连接授权服务器，已改用本地备份校验：" + err.Error()
	} else {
		status.Offline = true
		status.Reason = "未配置授权服务器，已改用本地备份校验"
	}

	s.verifyWithBackup(status.Devices, backup, devices, inTable)
	if status.Reason == "" || status.Offline {
		detail := summarizeDevices(status.Devices, len(devices))
		if detail != "" {
			status.Reason += "；" + detail
		}
	}
	return status
}

// reconcileWithServer binds free codes to unlicensed phones and records every
// binding it learns so the offline check keeps working later.
func (s *server) reconcileWithServer(ctx context.Context, out map[string]licenseDeviceState, backup *licenseBackup, devices []string, bindings []licenseBinding, serverURL string) {
	byCode := make(map[string]licenseBinding, len(bindings))
	boundTo := make(map[string]licenseBinding, len(bindings))
	used := make(map[string]bool, len(bindings))
	for _, binding := range bindings {
		byCode[binding.Code] = binding
		// A bound code is never free, even when it has expired — otherwise the
		// client would try to claim it again and report a misleading reason.
		if binding.BoundUDID != "" {
			used[binding.Code] = true
			// A phone may carry more than one binding (e.g. a renewed code);
			// always prefer a valid one over an expired one.
			if previous, exists := boundTo[binding.BoundUDID]; !exists ||
				(bindingExpired(previous.ExpiresAt) && !bindingExpired(binding.ExpiresAt)) {
				boundTo[binding.BoundUDID] = binding
			}
			// Only store a signature the backend actually produced; an unsigned
			// or tampered response must not become an offline credential.
			if binding.Exists && verifyBindingSignature(binding.Code, binding.BoundUDID, binding.ExpiresAt, binding.Signature) {
				backup.remember(binding.BoundUDID, binding.Code, binding.Signature, binding.ExpiresAt)
			}
		}
	}

	for _, udid := range devices {
		// The server is authoritative: if any code is already bound to this
		// phone it wins, even when the local backup was lost.
		if binding, ok := boundTo[udid]; ok &&
			verifyBindingSignature(binding.Code, udid, binding.ExpiresAt, binding.Signature) {
			if bindingExpired(binding.ExpiresAt) {
				out[udid] = licenseDeviceState{
					Code:      binding.Code,
					ExpiresAt: binding.ExpiresAt,
					Reason:    "授权码已过期，请联系管理员续期或更换授权码",
				}
				continue
			}
			if !binding.Enabled {
				out[udid] = licenseDeviceState{Code: binding.Code, ExpiresAt: binding.ExpiresAt, Reason: "该授权码已被停用"}
				continue
			}
			out[udid] = licenseDeviceState{
				Authorized: true,
				Code:       binding.Code,
				Source:     "server",
				ExpiresAt:  binding.ExpiresAt,
			}
			continue
		}

		// Claim the first free code in the table.
		claimed := ""
		for _, code := range sortedCodes(byCode) {
			binding := byCode[code]
			if !binding.Exists || !binding.Enabled || used[code] || binding.BoundUDID != "" {
				continue
			}
			signature, expiresAt, err := s.bindLicenseCode(ctx, serverURL, code, udid)
			if err != nil {
				out[udid] = licenseDeviceState{Reason: "绑定授权码失败：" + err.Error()}
				claimed = ""
				break
			}
			if !verifyBindingSignature(code, udid, expiresAt, signature) {
				out[udid] = licenseDeviceState{Reason: "授权服务器返回的签名无效，请检查服务器密钥"}
				claimed = ""
				break
			}
			used[code] = true
			backup.remember(udid, code, signature, expiresAt)
			out[udid] = licenseDeviceState{Authorized: true, Code: code, Source: "server", ExpiresAt: expiresAt}
			claimed = code
			break
		}
		if claimed != "" {
			continue
		}
		if _, exists := out[udid]; exists {
			continue
		}
		out[udid] = licenseDeviceState{Reason: "表格中没有可用的授权码，请联系管理员发码"}
	}
}

func sortedCodes(byCode map[string]licenseBinding) []string {
	codes := make([]string, 0, len(byCode))
	for code := range byCode {
		codes = append(codes, code)
	}
	// Stable order so the same phone always claims the same slot when several
	// codes are free.
	for i := 1; i < len(codes); i++ {
		for j := i; j > 0 && codes[j] < codes[j-1]; j-- {
			codes[j], codes[j-1] = codes[j-1], codes[j]
		}
	}
	return codes
}

// verifyWithBackup is the offline path. A phone is authorised only when the
// backup holds a code for it, that code is still in the table, and the stored
// signature verifies against the backend public key. Editing the JSON by hand
// therefore produces nothing usable.
func (s *server) verifyWithBackup(out map[string]licenseDeviceState, backup licenseBackup, devices []string, inTable map[string]bool) {
	for _, udid := range devices {
		entry, ok := backup.entryFor(udid)
		if !ok {
			out[udid] = licenseDeviceState{Reason: "本地备份中没有这台设备的授权记录"}
			continue
		}
		if !inTable[entry.Code] {
			out[udid] = licenseDeviceState{Code: entry.Code, Reason: "本地备份中的授权码已不在授权码表格中"}
			continue
		}
		if !verifyBindingSignature(entry.Code, udid, entry.ExpiresAt, entry.Signature) {
			out[udid] = licenseDeviceState{Code: entry.Code, Reason: "本地备份的签名校验失败，请联网重新绑定"}
			continue
		}
		if bindingExpired(entry.ExpiresAt) {
			out[udid] = licenseDeviceState{Code: entry.Code, ExpiresAt: entry.ExpiresAt, Reason: "授权码已过期，请联系管理员续期或更换授权码"}
			continue
		}
		out[udid] = licenseDeviceState{Authorized: true, Code: entry.Code, Source: "backup", ExpiresAt: entry.ExpiresAt}
	}
}

func summarizeDevices(states map[string]licenseDeviceState, total int) string {
	if total == 0 {
		return "当前没有连接设备"
	}
	ok := 0
	for _, state := range states {
		if state.Authorized {
			ok++
		}
	}
	if ok == total {
		return fmt.Sprintf("%d 台设备均已授权", ok)
	}
	return fmt.Sprintf("%d/%d 台设备已授权", ok, total)
}

type licenseBinding struct {
	Code      string `json:"code"`
	Exists    bool   `json:"exists"`
	Enabled   bool   `json:"enabled"`
	BoundUDID string `json:"boundUdid"`
	ExpiresAt string `json:"expiresAt"`
	Signature string `json:"signature"`
}

func (s *server) queryLicenseBindings(ctx context.Context, serverURL string, codes []string) ([]licenseBinding, error) {
	requestCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	endpoint := serverURL + "/api/v1/status?codes=" + url.QueryEscape(strings.Join(codes, ","))
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if token := s.licenseAPIToken(); token != "" {
		request.Header.Set("X-License-Token", token)
	}
	response, err := licenseHTTPClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("授权服务器返回 HTTP %d", response.StatusCode)
	}
	var payload struct {
		Codes []licenseBinding `json:"codes"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("解析授权服务器响应失败: %w", err)
	}
	return payload.Codes, nil
}

func (s *server) bindLicenseCode(ctx context.Context, serverURL, code, udid string) (signature string, expiresAt string, err error) {
	requestCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	body, err := json.Marshal(map[string]string{"code": code, "udid": udid})
	if err != nil {
		return "", "", err
	}
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, serverURL+"/api/v1/bind", bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	request.Header.Set("Content-Type", "application/json")
	if token := s.licenseAPIToken(); token != "" {
		request.Header.Set("X-License-Token", token)
	}
	response, err := licenseHTTPClient.Do(request)
	if err != nil {
		return "", "", err
	}
	defer response.Body.Close()
	var payload struct {
		OK        bool   `json:"ok"`
		Message   string `json:"message"`
		Signature string `json:"signature"`
		ExpiresAt string `json:"expiresAt"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return "", "", fmt.Errorf("解析绑定响应失败: %w", err)
	}
	if !payload.OK {
		if payload.Message == "" {
			payload.Message = "授权服务器拒绝了绑定"
		}
		return "", "", fmt.Errorf("%s", payload.Message)
	}
	return payload.Signature, payload.ExpiresAt, nil
}

func (s *server) handleLicenseStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.currentLicense())
}

// startLicenseWatcher reconciles once at startup and then periodically so the
// table, the server and the local backup stay in step while the app runs.
func (s *server) startLicenseWatcher(ctx context.Context) {
	_ = s.refreshLicense(ctx)
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = s.refreshLicense(ctx)
			}
		}
	}()
}
