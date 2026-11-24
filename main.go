package main

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"sync"
)

//go:embed web/*
var webFiles embed.FS

type server struct {
	configPath     string
	mu             sync.RWMutex
	config         Config
	sessions       map[string]string
	sessionURLs    map[string]string
	wdaSessions    map[string]bool
	wdaRelay       relaySet
	wdaStreamRelay relaySet
	windowSizes    map[string]windowSizeEntry
	deviceIDs      map[string]uint32
	httpClient     *http.Client
}

func main() {
	configPath, err := defaultConfigPath()
	if err != nil {
		log.Fatalf("启动失败：%s", chineseError(err))
	}
	cfg, err := loadConfig(configPath)
	if err != nil {
		log.Fatalf("启动失败：%s", chineseError(err))
	}

	s := &server{
		configPath:  configPath,
		config:      cfg,
		sessions:    make(map[string]string),
		sessionURLs: make(map[string]string),
		wdaSessions: make(map[string]bool),
		windowSizes: make(map[string]windowSizeEntry),
		deviceIDs:   make(map[string]uint32),
		httpClient: &http.Client{
			Timeout: runtimeCfg.HTTPTimeout,
			CheckRedirect: func(request *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("Appium 重定向次数过多")
				}
				return validateLocalURL(request.URL.String())
			},
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /api/devices", s.handleDevices)
	mux.HandleFunc("POST /api/batch", s.handleBatch)
	mux.HandleFunc("POST /api/devices/{udid}/settings", s.handleSettings)
	mux.HandleFunc("POST /api/devices/{udid}/session", s.handleCreateSession)
	mux.HandleFunc("DELETE /api/devices/{udid}/session", s.handleDeleteSession)
	mux.HandleFunc("GET /api/devices/{udid}/screenshot", s.handleScreenshot)
	mux.HandleFunc("GET /api/devices/{udid}/stream", s.handleStream)
	mux.HandleFunc("POST /api/devices/{udid}/tap", s.handleTap)
	mux.HandleFunc("POST /api/devices/{udid}/swipe", s.handleSwipe)
	mux.HandleFunc("POST /api/devices/{udid}/type", s.handleType)
	mux.HandleFunc("POST /api/devices/{udid}/actions", s.handleAction)
	mux.HandleFunc("GET /api/devices/{udid}/clipboard", s.handleGetClipboard)
	mux.HandleFunc("PUT /api/devices/{udid}/clipboard", s.handleSetClipboard)
	mux.HandleFunc("GET /api/devices/{udid}/messages", s.handleMessages)

	address := runtimeCfg.ListenAddress
	listener, err := net.Listen("tcp", address)
	if err != nil {
		log.Fatalf("无法启动本地服务：%s", chineseError(err))
	}
	log.Printf("海涛中控 v0.1 已启动：http://%s", address)
	log.Printf("配置文件位置：%s", configPath)
	openBrowser("http://" + address)
	if err := http.Serve(listener, s.withRequestLogging(s.localOnly(mux))); err != nil {
		log.Fatalf("本地服务异常退出：%s", chineseError(err))
	}
}

func openBrowser(address string) {
	if err := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", address).Start(); err != nil {
		log.Printf("无法自动打开浏览器，请手动访问 %s：%s", address, chineseError(err))
	}
}

func (s *server) localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		ip := net.ParseIP(strings.Trim(host, "[]"))
		if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
			http.Error(w, "loopback access only", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Host != r.Host || u.Scheme != "http" {
				http.Error(w, "cross-origin request rejected", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	data, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "web interface unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func requestUDID(r *http.Request) (string, error) {
	udid := r.PathValue("udid")
	if err := validateUDID(udid); err != nil {
		return "", err
	}
	return udid, nil
}

func validateUDID(udid string) error {
	if len(udid) < 8 || len(udid) > 128 {
		return errors.New("无效的设备 UDID")
	}
	for _, ch := range udid {
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.') {
			return errors.New("无效的设备 UDID")
		}
	}
	return nil
}

func validateLocalURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("Appium 地址必须是有效的本机 HTTP/HTTPS URL")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
		return errors.New("为保护设备与电脑，仅允许本机 Appium 地址（localhost 或 127.0.0.1）")
	}
	return nil
}

func decodeJSON(body io.Reader, target any) error {
	decoder := json.NewDecoder(io.LimitReader(body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("请求 JSON 无效: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("请求只能包含一个 JSON 对象")
	}
	return nil
}

func writeError(w http.ResponseWriter, status int, err error) {
	log.Printf("event=http_error status=%d error=%q", status, chineseError(err))
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func chineseError(err error) string {
	if err == nil {
		return "未知错误"
	}
	message := err.Error()
	replacer := strings.NewReplacer(
		"Unhandled endpoint:", "设备上的 WDA 不支持此操作接口：",
		"Actions list cannot be empty", "WDA 收到的触控动作列表为空",
		"no such element", "未找到对应的手机界面元素",
		"connection refused", "连接被拒绝，请检查相关服务是否已启动",
		"i/o timeout", "连接超时，请检查设备和服务状态",
		"timeout", "操作超时",
		"Appium 返回错误:", "Appium 返回错误：",
	)
	message = replacer.Replace(message)
	if !strings.ContainsAny(message, "中文错误失败无法设备连接操作手机服务启动超时") {
		return "操作失败，详细信息：" + message
	}
	return message
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func sessionPath(sessionID string) string {
	return "/session/" + url.PathEscape(sessionID)
}

func sessionIDFrom(result map[string]any) string {
	if id := stringValue(result["sessionId"]); id != "" {
		return id
	}
	if value, ok := result["value"].(map[string]any); ok {
		return stringValue(value["sessionId"])
	}
	return ""
}

func elementIDFrom(result map[string]any) string {
	value, ok := result["value"].(map[string]any)
	if ok {
		if id := stringValue(value["element-6066-11e4-a52e-4f735466cecf"]); id != "" {
			return id
		}
		if id := stringValue(value["ELEMENT"]); id != "" {
			return id
		}
	}
	if id := stringValue(result["ELEMENT"]); id != "" {
		return id
	}
	return ""
}

func stringValue(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	default:
		return ""
	}
}

func appiumErrorMessage(result map[string]any) string {
	if value, ok := result["value"].(map[string]any); ok {
		if message := stringValue(value["message"]); message != "" {
			return message
		}
		if name := stringValue(value["error"]); name != "" {
			return name
		}
	}
	return stringValue(result["message"])
}
