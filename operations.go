package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type tapInput struct {
	X           int `json:"x"`
	Y           int `json:"y"`
	ImageWidth  int `json:"imageWidth"`
	ImageHeight int `json:"imageHeight"`
}

type swipeInput struct {
	X1          int `json:"x1"`
	Y1          int `json:"y1"`
	X2          int `json:"x2"`
	Y2          int `json:"y2"`
	DurationMS  int `json:"durationMs"`
	ImageWidth  int `json:"imageWidth"`
	ImageHeight int `json:"imageHeight"`
}

type typeInput struct {
	Text string `json:"text"`
}

type actionInput struct {
	Action      string `json:"action"`
	Orientation string `json:"orientation"`
}

type clipboardInput struct {
	Text string `json:"text"`
}

type appInput struct {
	Action   string `json:"action"`
	BundleID string `json:"bundleId"`
}

type batchTarget struct {
	UDID string          `json:"udid"`
	Body json.RawMessage `json:"body,omitempty"`
}

type batchRequest struct {
	UDIDs     []string        `json:"udids,omitempty"`
	Operation string          `json:"operation"`
	Body      json.RawMessage `json:"body,omitempty"`
	Targets   []batchTarget   `json:"targets,omitempty"`
}

type normalizedBatchTarget struct {
	UDID    string
	Body    json.RawMessage
	HasBody bool
}

type batchResult struct {
	UDID  string `json:"udid"`
	Name  string `json:"name,omitempty"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Code  string `json:"code,omitempty"`
}

type batchResponse struct {
	OK        bool          `json:"ok"`
	Total     int           `json:"total"`
	Succeeded int           `json:"succeeded"`
	Failed    int           `json:"failed"`
	Results   []batchResult `json:"results"`
}

// encodeAppiumFrame converts an Appium/WDA screenshot into the JPEG frame
// used by the multipart fallback stream.
func encodeAppiumFrame(encoded string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("截图数据格式无效: %w", err)
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("无法识别截图格式: %w", err)
	}
	var frame bytes.Buffer
	if err := jpeg.Encode(&frame, img, &jpeg.Options{Quality: 72}); err != nil {
		return nil, fmt.Errorf("JPEG 编码失败: %w", err)
	}
	return frame.Bytes(), nil
}

func previewScreenshot(encoded string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("截图不是有效的 Base64: %w", err)
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("无法识别截图格式: %w", err)
	}
	thumb := resizeToWidth(img, 180)
	var frame bytes.Buffer
	if err := jpeg.Encode(&frame, thumb, &jpeg.Options{Quality: 60}); err != nil {
		return "", fmt.Errorf("编码预览图失败: %w", err)
	}
	return base64.StdEncoding.EncodeToString(frame.Bytes()), nil
}

func resizeToWidth(src image.Image, width int) image.Image {
	bounds := src.Bounds()
	if width <= 0 || bounds.Dx() <= width {
		return src
	}
	height := bounds.Dy() * width / bounds.Dx()
	if height < 1 {
		height = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		sourceY := bounds.Min.Y + y*bounds.Dy()/height
		for x := 0; x < width; x++ {
			sourceX := bounds.Min.X + x*bounds.Dx()/width
			dst.Set(x, y, src.At(sourceX, sourceY))
		}
	}
	return dst
}

func (s *server) tapDevice(ctx context.Context, udid, sessionID string, point tapInput) error {
	if err := validateImagePoint(point.X, point.Y, point.ImageWidth, point.ImageHeight); err != nil {
		return err
	}
	windowWidth, windowHeight, err := s.windowSize(ctx, udid, sessionID)
	if err != nil {
		return err
	}
	point.X, point.Y = scaleTouchPoint(point.X, point.Y, point.ImageWidth, point.ImageHeight, windowWidth, windowHeight)
	body := map[string]any{"actions": []any{map[string]any{
		"type": "pointer", "id": "finger1", "parameters": map[string]string{"pointerType": "touch"},
		"actions": []any{
			map[string]any{"type": "pointerMove", "duration": 0, "x": point.X, "y": point.Y},
			map[string]any{"type": "pointerDown", "button": 0},
			map[string]any{"type": "pointerUp", "button": 0},
		},
	}}}
	_, err = s.sessionRequest(ctx, udid, sessionID, http.MethodPost, sessionPath(sessionID)+"/actions", body)
	return err
}

func validateSwipeInput(gesture swipeInput) error {
	if err := validateImagePoint(gesture.X1, gesture.Y1, gesture.ImageWidth, gesture.ImageHeight); err != nil {
		return err
	}
	return validateImagePoint(gesture.X2, gesture.Y2, gesture.ImageWidth, gesture.ImageHeight)
}

func (s *server) swipeDevice(ctx context.Context, udid, sessionID string, gesture swipeInput) error {
	if err := validateSwipeInput(gesture); err != nil {
		return err
	}
	if gesture.DurationMS < 100 || gesture.DurationMS > 5000 {
		gesture.DurationMS = 500
	}
	windowWidth, windowHeight, err := s.windowSize(ctx, udid, sessionID)
	if err != nil {
		return err
	}
	gesture.X1, gesture.Y1 = scaleTouchPoint(gesture.X1, gesture.Y1, gesture.ImageWidth, gesture.ImageHeight, windowWidth, windowHeight)
	gesture.X2, gesture.Y2 = scaleTouchPoint(gesture.X2, gesture.Y2, gesture.ImageWidth, gesture.ImageHeight, windowWidth, windowHeight)
	body := map[string]any{"actions": []any{map[string]any{
		"type": "pointer", "id": "finger1", "parameters": map[string]string{"pointerType": "touch"},
		"actions": []any{
			map[string]any{"type": "pointerMove", "duration": 0, "x": gesture.X1, "y": gesture.Y1},
			map[string]any{"type": "pointerDown", "button": 0},
			map[string]any{"type": "pointerMove", "duration": gesture.DurationMS, "x": gesture.X2, "y": gesture.Y2},
			map[string]any{"type": "pointerUp", "button": 0},
		},
	}}}
	_, err = s.sessionRequest(ctx, udid, sessionID, http.MethodPost, sessionPath(sessionID)+"/actions", body)
	return err
}

func (s *server) windowSize(ctx context.Context, udid, sessionID string) (int, int, error) {
	now := time.Now()
	s.mu.RLock()
	cached, ok := s.windowSizes[udid]
	s.mu.RUnlock()
	if ok && cached.width > 0 && cached.height > 0 && now.Before(cached.expires) {
		return cached.width, cached.height, nil
	}

	result, err := s.sessionRequest(ctx, udid, sessionID, http.MethodGet, sessionPath(sessionID)+"/window/size", nil)
	if err != nil {
		return 0, 0, fmt.Errorf("获取手机触控区域失败: %w", err)
	}
	rect, ok := result["value"].(map[string]any)
	if !ok {
		return 0, 0, errors.New("Appium 未返回有效的手机窗口尺寸")
	}
	width, widthOK := positiveJSONInt(rect["width"])
	height, heightOK := positiveJSONInt(rect["height"])
	if !widthOK || !heightOK {
		return 0, 0, errors.New("Appium 返回的手机窗口尺寸无效")
	}
	s.mu.Lock()
	if s.windowSizes == nil {
		s.windowSizes = make(map[string]windowSizeEntry)
	}
	s.windowSizes[udid] = windowSizeEntry{width: width, height: height, expires: now.Add(runtimeCfg.WindowSizeTTL)}
	s.mu.Unlock()
	return width, height, nil
}

func positiveJSONInt(value any) (int, bool) {
	number, ok := value.(float64)
	if !ok || number <= 0 || number > 1<<20 || math.Trunc(number) != number {
		return 0, false
	}
	return int(number), true
}

func validateImagePoint(x, y, imageWidth, imageHeight int) error {
	if imageWidth <= 0 || imageHeight <= 0 || imageWidth > 1<<20 || imageHeight > 1<<20 {
		return errors.New("无效的屏幕截图尺寸")
	}
	if x < 0 || x >= imageWidth || y < 0 || y >= imageHeight {
		return errors.New("触控坐标超出屏幕截图范围")
	}
	return nil
}

func scaleTouchPoint(x, y, imageWidth, imageHeight, windowWidth, windowHeight int) (int, int) {
	scale := func(value, imageSize, windowSize int) int {
		point := int(math.Round(float64(value) * float64(windowSize) / float64(imageSize)))
		if point >= windowSize {
			return windowSize - 1
		}
		return point
	}
	return scale(x, imageWidth, windowWidth), scale(y, imageHeight, windowHeight)
}

func validateTypeInput(input typeInput) error {
	if len(input.Text) > 4000 {
		return errors.New("输入内容不能超过 4000 字节")
	}
	return nil
}

func (s *server) typeDevice(ctx context.Context, udid, sessionID string, input typeInput) error {
	if err := validateTypeInput(input); err != nil {
		return err
	}
	active, err := s.sessionRequest(ctx, udid, sessionID, http.MethodGet, sessionPath(sessionID)+"/element/active", nil)
	if err != nil {
		return fmt.Errorf("获取当前输入框失败（请先在手机上聚焦文本框）: %w", err)
	}
	elementID := elementIDFrom(active)
	if elementID == "" {
		return errors.New("当前没有可输入文本的活动控件")
	}
	_, err = s.sessionRequest(ctx, udid, sessionID, http.MethodPost, sessionPath(sessionID)+"/element/"+url.PathEscape(elementID)+"/value", map[string]any{
		"text": input.Text,
	})
	return err
}

func validateActionInput(input actionInput) error {
	switch input.Action {
	case "lock", "unlock", "home", "volumeUp", "volumeDown", "reboot", "enter":
		return nil
	case "orientation":
		orientation := strings.ToUpper(input.Orientation)
		if orientation != "PORTRAIT" && orientation != "LANDSCAPE" {
			return errors.New("方向仅支持 PORTRAIT 或 LANDSCAPE")
		}
		return nil
	default:
		return errors.New("不支持的设备操作")
	}
}

func (s *server) actionDevice(ctx context.Context, udid, sessionID string, input actionInput) error {
	if err := validateActionInput(input); err != nil {
		return err
	}
	var endpoint string
	var body any
	switch input.Action {
	case "lock", "unlock":
		if s.isWDASession(udid) {
			endpoint = "/wda/" + input.Action
		} else {
			endpoint = sessionPath(sessionID) + "/execute/sync"
			body = map[string]any{"script": "mobile: " + input.Action, "args": []any{}}
		}
	case "home":
		if s.isWDASession(udid) {
			endpoint = "/wda/homescreen"
		} else {
			endpoint = sessionPath(sessionID) + "/execute/sync"
			body = map[string]any{"script": "mobile: pressButton", "args": []any{map[string]string{"name": "home"}}}
		}
	case "volumeUp", "volumeDown":
		buttonName := "volumeUp"
		if input.Action == "volumeDown" {
			buttonName = "volumeDown"
		}
		if s.isWDASession(udid) {
			endpoint = "/wda/pressButton"
			body = map[string]string{"name": buttonName}
		} else {
			endpoint = sessionPath(sessionID) + "/execute/sync"
			body = map[string]any{"script": "mobile: pressButton", "args": []any{map[string]string{"name": buttonName}}}
		}
	case "reboot":
		if s.isWDASession(udid) {
			return unsupportedError("当前 WDA 直连模式不支持重启")
		}
		endpoint = sessionPath(sessionID) + "/execute/sync"
		body = map[string]any{"script": "mobile: reboot", "args": []any{}}
	case "orientation":
		endpoint = sessionPath(sessionID) + "/orientation"
		body = map[string]string{"orientation": strings.ToUpper(input.Orientation)}
	case "enter":
		endpoint = sessionPath(sessionID) + "/actions"
		body = map[string]any{
			"actions": []any{
				map[string]any{
					"type": "key",
					"id":   "keyboard",
					"actions": []any{
						map[string]any{"type": "keyDown", "value": "\uE007"},
						map[string]any{"type": "keyUp", "value": "\uE007"},
					},
				},
			},
		}
	}
	if _, err := s.sessionRequest(ctx, udid, sessionID, http.MethodPost, endpoint, body); err != nil {
		return err
	}
	if input.Action == "orientation" {
		s.clearWindowSize(udid)
	}
	return nil
}

func validateClipboardInput(input clipboardInput) error {
	if len(input.Text) > 10000 {
		return errors.New("剪贴板内容不能超过 10000 字节")
	}
	return nil
}

func (s *server) setClipboardDevice(ctx context.Context, udid, sessionID string, input clipboardInput) error {
	if err := validateClipboardInput(input); err != nil {
		return err
	}
	endpoint := sessionPath(sessionID) + "/appium/device/set_clipboard"
	body := map[string]string{"content": base64String(input.Text), "contentType": "text/plain", "label": runtimeCfg.ClientName}
	if s.isWDASession(udid) {
		endpoint = "/wda/setPasteboard"
		body = map[string]string{"content": base64String(input.Text), "uti": "public.utf8-plain-text"}
	}
	_, err := s.sessionRequest(ctx, udid, sessionID, http.MethodPost, endpoint, body)
	return err
}

// normalizeBatchTargets converts both the legacy "udids + shared body" form and
// the per-device "targets" form into one list. A target without its own body
// inherits the request-level body, so a template can be shared with overrides.
func normalizeBatchTargets(request batchRequest) ([]normalizedBatchTarget, error) {
	if len(request.Targets) == 0 {
		targets := make([]normalizedBatchTarget, 0, len(request.UDIDs))
		for _, udid := range request.UDIDs {
			targets = append(targets, normalizedBatchTarget{
				UDID:    udid,
				Body:    request.Body,
				HasBody: len(bytes.TrimSpace(request.Body)) > 0,
			})
		}
		return targets, nil
	}

	targets := make([]normalizedBatchTarget, 0, len(request.Targets))
	for _, target := range request.Targets {
		hasBody := len(bytes.TrimSpace(target.Body)) > 0
		body := target.Body
		if !hasBody {
			body = request.Body
		}
		targets = append(targets, normalizedBatchTarget{
			UDID:    target.UDID,
			Body:    body,
			HasBody: hasBody || len(bytes.TrimSpace(request.Body)) > 0,
		})
	}
	return targets, nil
}

func validateBatchTargets(targets []normalizedBatchTarget, sharedBody json.RawMessage) error {
	if len(targets) == 0 {
		return errors.New("批量操作至少需要一台设备")
	}
	if len(targets) > runtimeCfg.MaxBatchDevices {
		return fmt.Errorf("批量操作最多支持 %d 台设备", runtimeCfg.MaxBatchDevices)
	}
	seen := make(map[string]bool, len(targets))
	for _, target := range targets {
		if err := validateUDID(target.UDID); err != nil {
			return err
		}
		if seen[target.UDID] {
			return fmt.Errorf("设备 %s 在批量请求中重复出现", target.UDID)
		}
		seen[target.UDID] = true
		if !target.HasBody && len(bytes.TrimSpace(sharedBody)) == 0 {
			return fmt.Errorf("设备 %s 缺少操作内容", target.UDID)
		}
	}
	return nil
}

func validateAppInput(input appInput) error {
	switch input.Action {
	case "open", "uninstall":
	default:
		return errors.New("应用操作仅支持 open 或 uninstall")
	}
	bundleID := strings.TrimSpace(input.BundleID)
	if bundleID == "" {
		return errors.New("缺少应用 Bundle ID")
	}
	if len(bundleID) > 200 {
		return errors.New("Bundle ID 不能超过 200 字节")
	}
	for _, r := range bundleID {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_' {
			continue
		}
		return errors.New("Bundle ID 只能包含字母、数字、点和连字符")
	}
	return nil
}

func (s *server) appDevice(ctx context.Context, udid, sessionID string, input appInput) error {
	if err := validateAppInput(input); err != nil {
		return err
	}
	bundleID := strings.TrimSpace(input.BundleID)
	var endpoint string
	var body any
	if s.isWDASession(udid) {
		if input.Action == "uninstall" {
			return unsupportedError("当前 WDA 直连模式不支持卸载应用，请使用 Appium 会话")
		}
		endpoint = "/wda/apps/launch"
		body = map[string]string{"bundleId": bundleID}
	} else {
		// Appium XCUITest exposes app management through mobile: commands,
		// consistent with the other session operations in this project.
		endpoint = sessionPath(sessionID) + "/execute/sync"
		script := "mobile: launchApp"
		if input.Action == "uninstall" {
			script = "mobile: removeApp"
		}
		body = map[string]any{
			"script": script,
			"args":   []any{map[string]string{"bundleId": bundleID}},
		}
	}
	_, err := s.sessionRequest(ctx, udid, sessionID, http.MethodPost, endpoint, body)
	return err
}

func supportedBatchOperation(operation string) bool {
	switch operation {
	case "tap", "swipe", "type", "actions", "action", "clipboard", "app":
		return true
	default:
		return false
	}
}

func parseBatchPayload(operation string, raw json.RawMessage) (any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.New("批量操作缺少 body")
	}
	decode := func(target any) error {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(target); err != nil {
			return fmt.Errorf("批量参数无效: %w", err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return errors.New("批量参数只能包含一个 JSON 对象")
		}
		return nil
	}

	switch operation {
	case "tap":
		var input tapInput
		if err := decode(&input); err != nil {
			return nil, err
		}
		if err := validateImagePoint(input.X, input.Y, input.ImageWidth, input.ImageHeight); err != nil {
			return nil, err
		}
		return input, nil
	case "swipe":
		var input swipeInput
		if err := decode(&input); err != nil {
			return nil, err
		}
		if err := validateSwipeInput(input); err != nil {
			return nil, err
		}
		return input, nil
	case "type":
		var input typeInput
		if err := decode(&input); err != nil {
			return nil, err
		}
		if err := validateTypeInput(input); err != nil {
			return nil, err
		}
		return input, nil
	case "actions", "action":
		var input actionInput
		if err := decode(&input); err != nil {
			return nil, err
		}
		if err := validateActionInput(input); err != nil {
			return nil, err
		}
		return input, nil
	case "clipboard":
		var input clipboardInput
		if err := decode(&input); err != nil {
			return nil, err
		}
		if err := validateClipboardInput(input); err != nil {
			return nil, err
		}
		return input, nil
	case "app":
		var input appInput
		if err := decode(&input); err != nil {
			return nil, err
		}
		if err := validateAppInput(input); err != nil {
			return nil, err
		}
		return input, nil
	default:
		return nil, errors.New("不支持的批量操作")
	}
}

func (s *server) performBatchOperation(ctx context.Context, udid, sessionID, operation string, payload any) error {
	switch operation {
	case "tap":
		input, ok := payload.(tapInput)
		if !ok {
			return errors.New("批量参数类型错误")
		}
		return s.tapDevice(ctx, udid, sessionID, input)
	case "swipe":
		input, ok := payload.(swipeInput)
		if !ok {
			return errors.New("批量参数类型错误")
		}
		return s.swipeDevice(ctx, udid, sessionID, input)
	case "type":
		input, ok := payload.(typeInput)
		if !ok {
			return errors.New("批量参数类型错误")
		}
		return s.typeDevice(ctx, udid, sessionID, input)
	case "actions", "action":
		input, ok := payload.(actionInput)
		if !ok {
			return errors.New("批量参数类型错误")
		}
		return s.actionDevice(ctx, udid, sessionID, input)
	case "clipboard":
		input, ok := payload.(clipboardInput)
		if !ok {
			return errors.New("批量参数类型错误")
		}
		return s.setClipboardDevice(ctx, udid, sessionID, input)
	case "app":
		input, ok := payload.(appInput)
		if !ok {
			return errors.New("批量参数类型错误")
		}
		return s.appDevice(ctx, udid, sessionID, input)
	default:
		return errors.New("不支持的批量操作")
	}
}
