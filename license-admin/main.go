package main

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed web/*
var webFS embed.FS

// codeAlphabet omits characters that are easy to confuse when typed by hand
// (I, O, 0, 1).
const codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

type app struct {
	templates    *template.Template
	username     string
	password     string
	apiToken     string
	records      *recordStore
	sessions     *sessionStore
	signer       *signer
	loginLimiter *attemptLimiter
	bindLimiter  *windowLimiter
	xlsxPath     string
	listenAddr   string
	// lastBatch keeps the codes produced by the most recent generation so the
	// admin can download exactly that batch as an Excel file.
	batchMu     sync.Mutex
	lastBatch   []Record
	lastNote    string
	lastBatchAt time.Time
}

func (a *app) rememberBatch(records []Record, note string) {
	a.batchMu.Lock()
	defer a.batchMu.Unlock()
	a.lastBatch = append([]Record(nil), records...)
	a.lastNote = note
	a.lastBatchAt = time.Now()
}

func (a *app) currentBatch() ([]Record, string, time.Time) {
	a.batchMu.Lock()
	defer a.batchMu.Unlock()
	return append([]Record(nil), a.lastBatch...), a.lastNote, a.lastBatchAt
}

// newLicenseCode returns a random XXXX-XXXX-XXXX-XXXX code.
func newLicenseCode() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	for i := range buf {
		buf[i] = codeAlphabet[int(buf[i])%len(codeAlphabet)]
	}
	s := string(buf)
	return s[0:4] + "-" + s[4:8] + "-" + s[8:12] + "-" + s[12:16], nil
}

// normalizeCode accepts codes with or without dashes and any case.
func normalizeCode(code string) string {
	cleaned := strings.ToUpper(strings.TrimSpace(code))
	cleaned = strings.ReplaceAll(cleaned, "-", "")
	cleaned = strings.ReplaceAll(cleaned, " ", "")
	if len(cleaned) != 16 {
		return ""
	}
	return cleaned[0:4] + "-" + cleaned[4:8] + "-" + cleaned[8:12] + "-" + cleaned[12:16]
}

func validateUDID(udid string) error {
	udid = strings.TrimSpace(udid)
	if udid == "" {
		return errors.New("缺少设备 UDID")
	}
	if len(udid) < 6 || len(udid) > 64 {
		return errors.New("UDID 长度不合法")
	}
	for _, r := range udid {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r == '-':
		default:
			return errors.New("UDID 含有非法字符")
		}
	}
	return nil
}

type Record struct {
	Code      string     `json:"code"`
	Note      string     `json:"note"`
	CreatedAt time.Time  `json:"createdAt"`
	BoundUDID string     `json:"boundUdid,omitempty"`
	BoundAt   *time.Time `json:"boundAt,omitempty"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	Enabled   bool       `json:"enabled"`
}

// expired reports whether the code is past its validity period. A nil ExpiresAt
// means the code never expires.
func (r Record) expired(now time.Time) bool {
	return r.ExpiresAt != nil && now.After(*r.ExpiresAt)
}

func (r Record) expiresAtText() string {
	if r.ExpiresAt == nil {
		return ""
	}
	return r.ExpiresAt.Format("2006-01-02 15:04:05")
}

// StatusText/StatusClass/ExpiresText are used by the admin template.
func (r Record) StatusText() string {
	switch {
	case !r.Enabled:
		return "已停用"
	case r.expired(time.Now()):
		return "已过期"
	case r.BoundUDID != "":
		return "已绑定"
	default:
		return "未绑定"
	}
}

func (r Record) StatusClass() string {
	switch r.StatusText() {
	case "已绑定":
		return "on"
	case "已过期":
		return "expired"
	case "已停用":
		return "off"
	default:
		return ""
	}
}

func (r Record) ExpiresText() string {
	if r.ExpiresAt == nil {
		return "永久"
	}
	return r.ExpiresAt.Format("2006-01-02 15:04")
}

type recordStore struct {
	mu      sync.Mutex
	path    string
	records []Record
	limit   int
}

func newRecordStore(path string) (*recordStore, error) {
	store := &recordStore{path: path, limit: 5000}
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return store, nil
	}
	if err := json.Unmarshal(data, &store.records); err != nil {
		return nil, fmt.Errorf("解析记录文件失败: %w", err)
	}
	for i := range store.records {
		store.records[i].Code = normalizeCode(store.records[i].Code)
	}
	return store, nil
}

func (s *recordStore) snapshot() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Record(nil), s.records...)
}

func (s *recordStore) find(code string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range s.records {
		if record.Code == code {
			return record, true
		}
	}
	return Record{}, false
}

// generate appends count new codes and returns them. days > 0 sets a validity
// period; days <= 0 means the codes never expire.
func (s *recordStore) generate(count int, note string, days int) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	var expiresAt *time.Time
	if days > 0 {
		expiry := now.AddDate(0, 0, days)
		expiresAt = &expiry
	}
	created := make([]Record, 0, count)
	for i := 0; i < count; i++ {
		code, err := newLicenseCode()
		if err != nil {
			return nil, err
		}
		for s.existsLocked(code) {
			if code, err = newLicenseCode(); err != nil {
				return nil, err
			}
		}
		record := Record{Code: code, Note: note, CreatedAt: now, ExpiresAt: expiresAt, Enabled: true}
		created = append(created, record)
		s.records = append(s.records, record)
	}
	if len(s.records) > s.limit {
		s.records = s.records[len(s.records)-s.limit:]
	}
	return created, s.saveLocked()
}

func (s *recordStore) existsLocked(code string) bool {
	for _, record := range s.records {
		if record.Code == code {
			return true
		}
	}
	return false
}

// bind locks a code to a device. Rebinding the same device is idempotent; a
// different device is rejected so one code only ever serves one phone.
func (s *recordStore) bind(code, udid string) (Record, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.records {
		record := &s.records[i]
		if record.Code != code {
			continue
		}
		if !record.Enabled {
			return *record, "该授权码已被停用", nil
		}
		if record.expired(time.Now()) {
			return *record, "该授权码已过期", nil
		}
		if record.BoundUDID == udid {
			return *record, "", nil
		}
		if record.BoundUDID != "" {
			return *record, "该授权码已绑定其他设备", nil
		}
		now := time.Now()
		record.BoundUDID = udid
		record.BoundAt = &now
		return *record, "", s.saveLocked()
	}
	return Record{}, "授权码不存在", nil
}

func (s *recordStore) unbind(code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.records {
		if s.records[i].Code == code {
			s.records[i].BoundUDID = ""
			s.records[i].BoundAt = nil
			return s.saveLocked()
		}
	}
	return errors.New("授权码不存在")
}

func (s *recordStore) remove(code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.records[:0]
	for _, record := range s.records {
		if record.Code == code {
			continue
		}
		kept = append(kept, record)
	}
	s.records = kept
	return s.saveLocked()
}

func (s *recordStore) saveLocked() error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.records, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

type sessionStore struct {
	mu     sync.Mutex
	tokens map[string]time.Time
	ttl    time.Duration
}

func newSessionStore(ttl time.Duration) *sessionStore {
	return &sessionStore{tokens: make(map[string]time.Time), ttl: ttl}
}

func (s *sessionStore) create() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[token] = time.Now().Add(s.ttl)
	return token, nil
}

func (s *sessionStore) valid(token string) bool {
	if token == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	expiry, ok := s.tokens[token]
	if !ok {
		return false
	}
	if time.Now().After(expiry) {
		delete(s.tokens, token)
		return false
	}
	return true
}

func (s *sessionStore) drop(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, token)
}

func (a *app) isAuthed(r *http.Request) bool {
	cookie, err := r.Cookie("license_admin")
	if err != nil {
		return false
	}
	return a.sessions.valid(cookie.Value)
}

func (a *app) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.isAuthed(r) {
			if strings.HasPrefix(r.URL.Path, "/admin/api/") {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "登录已过期，请重新登录"})
				return
			}
			http.Redirect(w, r, "/admin/login/?next="+r.URL.Path, http.StatusFound)
			return
		}
		next(w, r)
	}
}

// requireToken guards the client-facing API. When no token is configured the
// endpoints stay open so a fresh install works without extra setup.
func (a *app) requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.apiToken != "" {
			got := r.Header.Get("X-License-Token")
			if subtle.ConstantTimeCompare([]byte(got), []byte(a.apiToken)) != 1 {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "授权接口令牌不正确"})
				return
			}
		}
		next(w, r)
	}
}

func (a *app) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/admin/", http.StatusFound)
}

func (a *app) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if a.isAuthed(r) {
		http.Redirect(w, r, "/admin/", http.StatusFound)
		return
	}
	a.render(w, "login.html", map[string]any{
		"Next":   r.URL.Query().Get("next"),
		"Error":  r.URL.Query().Get("error") == "1",
		"Locked": r.URL.Query().Get("locked") == "1",
	})
}

// remoteIP identifies the caller for rate limiting. Behind a local reverse
// proxy the peer is 127.0.0.1, so the forwarded header is trusted only in that
// case — a remote client cannot spoof its address.
func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			first := strings.TrimSpace(strings.Split(forwarded, ",")[0])
			if net.ParseIP(first) != nil {
				return first
			}
		}
	}
	return host
}

func (a *app) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "请求无效", http.StatusBadRequest)
		return
	}
	next := r.PostFormValue("next")
	if next == "" || !strings.HasPrefix(next, "/admin/") {
		next = "/admin/"
	}
	clientIP := remoteIP(r)
	if !a.loginLimiter.allow(clientIP) {
		http.Redirect(w, r, "/admin/login/?locked=1&next="+next, http.StatusFound)
		return
	}
	userOK := subtle.ConstantTimeCompare([]byte(r.PostFormValue("username")), []byte(a.username)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(r.PostFormValue("password")), []byte(a.password)) == 1
	if !userOK || !passOK {
		a.loginLimiter.fail(clientIP)
		http.Redirect(w, r, "/admin/login/?error=1&next="+next, http.StatusFound)
		return
	}
	a.loginLimiter.reset(clientIP)
	token, err := a.sessions.create()
	if err != nil {
		http.Error(w, "创建会话失败", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "license_admin",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int((12 * time.Hour).Seconds()),
	})
	http.Redirect(w, r, next, http.StatusFound)
}

func (a *app) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("license_admin"); err == nil {
		a.sessions.drop(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: "license_admin", Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	http.Redirect(w, r, "/admin/login/", http.StatusFound)
}

func (a *app) handleIndex(w http.ResponseWriter, r *http.Request) {
	records := a.records.snapshot()
	bound := 0
	for _, record := range records {
		if record.BoundUDID != "" {
			bound++
		}
	}
	a.render(w, "index.html", map[string]any{
		"Records":  records,
		"Username": a.username,
		"Total":    len(records),
		"Bound":    bound,
		"XlsxPath": a.xlsxPath,
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// tableRows renders records as sheet rows.
func tableRows(records []Record) [][]string {
	rows := [][]string{{"授权码", "备注", "绑定设备", "绑定时间", "到期时间", "生成时间", "状态"}}
	now := time.Now()
	for _, record := range records {
		boundAt := ""
		if record.BoundAt != nil {
			boundAt = record.BoundAt.Format("2006-01-02 15:04:05")
		}
		expires := record.expiresAtText()
		if expires == "" {
			expires = "永久"
		}
		status := "未绑定"
		if record.BoundUDID != "" {
			status = "已绑定"
		}
		if !record.Enabled {
			status = "已停用"
		}
		if record.expired(now) {
			status = "已过期"
		}
		rows = append(rows, []string{
			record.Code,
			record.Note,
			record.BoundUDID,
			boundAt,
			expires,
			record.CreatedAt.Format("2006-01-02 15:04:05"),
			status,
		})
	}
	return rows
}

// writeTable regenerates 授权码.xlsx from the current records.
func (a *app) writeTable() error {
	return writeXLSX(a.xlsxPath, "授权码", tableRows(a.records.snapshot()))
}

func (a *app) handleGenerate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Count int    `json:"count"`
		Note  string `json:"note"`
		Days  int    `json:"days"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求 JSON 无效"})
		return
	}
	if body.Count <= 0 {
		body.Count = 1
	}
	if body.Count > 500 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "单次最多生成 500 个"})
		return
	}
	if body.Days < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "有效期天数不能为负数"})
		return
	}
	if body.Days > 36500 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "有效期最长 36500 天"})
		return
	}
	created, err := a.records.generate(body.Count, strings.TrimSpace(body.Note), body.Days)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := a.writeTable(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "生成表格失败：" + err.Error()})
		return
	}
	codes := make([]string, 0, len(created))
	for _, record := range created {
		codes = append(codes, record.Code)
	}
	a.rememberBatch(created, strings.TrimSpace(body.Note))
	writeJSON(w, http.StatusOK, map[string]any{
		"codes":    codes,
		"table":    absPath(a.xlsxPath),
		"download": "/admin/export.xlsx?batch=last",
		"filename": batchFilename(strings.TrimSpace(body.Note), time.Now()),
	})
}

func (a *app) handleUnbind(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求 JSON 无效"})
		return
	}
	code := normalizeCode(body.Code)
	if code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "授权码格式不正确"})
		return
	}
	if err := a.records.unbind(code); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	_ = a.writeTable()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *app) handleDeleteRecord(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求 JSON 无效"})
		return
	}
	code := normalizeCode(body.Code)
	if code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "授权码格式不正确"})
		return
	}
	if err := a.records.remove(code); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	_ = a.writeTable()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleBind is the client-facing binding endpoint. The first call locks the
// code to that UDID and returns the backend result.
func (a *app) handleBind(w http.ResponseWriter, r *http.Request) {
	if !a.bindLimiter.allow(remoteIP(r)) {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"ok": false, "message": "请求过于频繁，请稍后再试"})
		return
	}
	var body struct {
		Code string `json:"code"`
		UDID string `json:"udid"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "请求 JSON 无效"})
		return
	}
	code := normalizeCode(body.Code)
	if code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "授权码格式不正确"})
		return
	}
	if err := validateUDID(body.UDID); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	udid := strings.TrimSpace(body.UDID)
	record, reason, err := a.records.bind(code, udid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	if reason != "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":        false,
			"code":      code,
			"message":   reason,
			"boundUdid": record.BoundUDID,
		})
		return
	}
	_ = a.writeTable()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"code":      record.Code,
		"udid":      record.BoundUDID,
		"message":   "绑定成功",
		"expiresAt": canonicalExpiry(record.ExpiresAt),
		"signature": a.signer.signBinding(record.Code, record.BoundUDID, record.ExpiresAt),
	})
}

// handleStatus reports the binding state of the codes a client presents, which
// is what the client compares its table scan against.
func (a *app) handleStatus(w http.ResponseWriter, r *http.Request) {
	type item struct {
		Code      string `json:"code"`
		Exists    bool   `json:"exists"`
		Enabled   bool   `json:"enabled"`
		BoundUDID string `json:"boundUdid,omitempty"`
		ExpiresAt string `json:"expiresAt,omitempty"`
		Signature string `json:"signature,omitempty"`
	}
	raw := r.URL.Query().Get("codes")
	items := make([]item, 0, 8)
	now := time.Now()
	for _, part := range strings.Split(raw, ",") {
		code := normalizeCode(part)
		if code == "" {
			continue
		}
		record, ok := a.records.find(code)
		entry := item{Code: code, Exists: ok}
		if ok {
			entry.Enabled = record.Enabled
			entry.BoundUDID = record.BoundUDID
			entry.ExpiresAt = canonicalExpiry(record.ExpiresAt)
			// An expired code is reported as unusable even though the binding
			// itself still exists.
			if record.expired(now) {
				entry.Enabled = false
			}
			// Sign on the fly so bindings made before signatures existed are
			// still usable offline.
			if record.BoundUDID != "" {
				entry.Signature = a.signer.signBinding(record.Code, record.BoundUDID, record.ExpiresAt)
			}
		}
		items = append(items, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"codes": items})
}

func (a *app) handleExportCSV(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="license-codes.csv"`)
	_, _ = w.Write([]byte("\xEF\xBB\xBF"))
	writer := csv.NewWriter(w)
	for _, row := range tableRows(a.records.snapshot()) {
		// CSV export drops the trailing status column to keep the old layout.
		_ = writer.Write(row[:6])
	}
	writer.Flush()
}

func (a *app) handleExportXLSX(w http.ResponseWriter, r *http.Request) {
	records := a.records.snapshot()
	filename := "授权码.xlsx"
	if r.URL.Query().Get("batch") == "last" {
		batch, note, at := a.currentBatch()
		if len(batch) == 0 {
			http.Error(w, "还没有生成过授权码", http.StatusNotFound)
			return
		}
		records = batch
		filename = batchFilename(note, at)
	}
	data, err := buildXLSX("授权码", tableRows(records))
	if err != nil {
		http.Error(w, "生成表格失败", http.StatusInternalServerError)
		return
	}
	writeAttachment(w, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", filename, data)
}

// batchFilename names the per-generation workbook after the customer note and
// the moment it was produced, so the file is self-describing when sent out.
func batchFilename(note string, at time.Time) string {
	stamp := at.Format("20060102-1504")
	clean := sanitizeFilename(note)
	if clean == "" {
		return "授权码-" + stamp + ".xlsx"
	}
	return "授权码-" + clean + "-" + stamp + ".xlsx"
}

func sanitizeFilename(name string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		case r > 127: // keep CJK and other non-ASCII letters readable
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// writeAttachment sets a UTF-8 safe filename and streams the payload.
func writeAttachment(w http.ResponseWriter, contentType, filename string, data []byte) {
	fallback := "license-codes.xlsx"
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, fallback, url.PathEscape(filename)))
	_, _ = w.Write(data)
}

// handlePublicKey lets the developer confirm which key the client must embed.
func (a *app) handlePublicKey(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"publicKey": a.signer.publicHex()})
}

func (a *app) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, must-revalidate")
	if err := a.templates.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("render %s: %v", name, err)
	}
}

func randomPassword() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "ChangeMe12345"
	}
	for i := range buf {
		buf[i] = codeAlphabet[int(buf[i])%len(codeAlphabet)]
	}
	return string(buf)
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8090", "监听地址")
	dataFile := flag.String("data", "records.json", "授权码数据文件")
	xlsxFile := flag.String("xlsx", "授权码.xlsx", "输出的授权码表格路径")
	keyFile := flag.String("key", "", "绑定签名私钥文件（默认放在程序目录，首次启动自动生成）")
	flag.Parse()

	// Resolve the key next to the executable so running from a different
	// working directory cannot silently create a second, incompatible key.
	keyPath := *keyFile
	if keyPath == "" {
		keyPath = filepath.Join(executableDir(), "signing.key")
	}

	password := os.Getenv("LICENSE_ADMIN_PASSWORD")
	generated := false
	if password == "" {
		password = randomPassword()
		generated = true
	}
	apiToken := strings.TrimSpace(os.Getenv("LICENSE_API_TOKEN"))

	// A public listener without an API token lets anyone burn the code pool,
	// so refuse that combination instead of shipping a footgun.
	if apiToken == "" && !isLoopbackAddr(*addr) {
		log.Fatalf("拒绝启动：对外监听 %s 时必须设置环境变量 LICENSE_API_TOKEN", *addr)
	}

	signKey, err := loadOrCreateSigner(keyPath)
	if err != nil {
		log.Fatalf("加载签名密钥失败：%s", err)
	}

	records, err := newRecordStore(*dataFile)
	if err != nil {
		log.Fatalf("启动失败：%s", err)
	}
	templates, err := template.ParseFS(webFS, "web/*.html")
	if err != nil {
		log.Fatalf("加载模板失败：%s", err)
	}

	a := &app{
		templates:    templates,
		username:     "admin",
		password:     password,
		apiToken:     apiToken,
		records:      records,
		sessions:     newSessionStore(12 * time.Hour),
		signer:       signKey,
		loginLimiter: newAttemptLimiter(5, 15*time.Minute, 15*time.Minute),
		bindLimiter:  newWindowLimiter(30, time.Minute),
		xlsxPath:     *xlsxFile,
		listenAddr:   *addr,
	}

	// Keep the table in sync with the stored records on every start.
	if err := a.writeTable(); err != nil {
		log.Printf("警告：写入授权码表格失败：%v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", a.handleRoot)
	mux.HandleFunc("GET /admin/login/", a.handleLoginPage)
	mux.HandleFunc("POST /admin/login/", a.handleLoginSubmit)
	mux.HandleFunc("GET /admin/logout/", a.handleLogout)
	mux.HandleFunc("GET /admin/", a.requireAuth(a.handleIndex))
	mux.HandleFunc("POST /admin/api/generate", a.requireAuth(a.handleGenerate))
	mux.HandleFunc("POST /admin/api/unbind", a.requireAuth(a.handleUnbind))
	mux.HandleFunc("POST /admin/api/records/delete", a.requireAuth(a.handleDeleteRecord))
	mux.HandleFunc("GET /admin/export.csv", a.requireAuth(a.handleExportCSV))
	mux.HandleFunc("GET /admin/export.xlsx", a.requireAuth(a.handleExportXLSX))
	// Client-facing API: no admin session, optional shared token.
	mux.HandleFunc("POST /api/v1/bind", a.requireToken(a.handleBind))
	mux.HandleFunc("GET /api/v1/status", a.requireToken(a.handleStatus))
	mux.HandleFunc("GET /api/v1/pubkey", a.handlePublicKey)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(mustSub(webFS, "web")))))

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("启动失败：%v", err)
	}
	log.Printf("授权码管理站已启动：http://%s/admin/", listener.Addr().String())
	log.Printf("授权码表格：%s", absPath(*xlsxFile))
	log.Printf("签名密钥：%s", absPath(keyPath))
	log.Printf("绑定签名公钥：%s", signKey.publicHex())
	log.Printf("登录用户名：admin")
	if generated {
		log.Printf("本次随机生成的管理员密码：%s （可用环境变量 LICENSE_ADMIN_PASSWORD 固定）", password)
	}
	if apiToken == "" {
		log.Printf("提示：未设置 LICENSE_API_TOKEN，当前仅允许回环地址访问授权接口")
	}
	if err := http.Serve(listener, mux); err != nil {
		log.Fatalf("服务异常退出：%v", err)
	}
}

func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func absPath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

func mustSub(fsys embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		log.Fatalf("加载静态资源失败：%v", err)
	}
	return sub
}

func executableDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}
