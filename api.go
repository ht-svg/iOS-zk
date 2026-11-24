package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

func (s *server) handleScreenshot(w http.ResponseWriter, r *http.Request) {
	udid, sessionID, ok := s.sessionOrError(w, r)
	if !ok {
		return
	}
	result, err := s.sessionRequest(r.Context(), udid, sessionID, http.MethodGet, sessionPath(sessionID)+"/screenshot", nil)
	if err != nil {
		s.writeAPIError(w, http.StatusBadGateway, err)
		return
	}
	value := stringValue(result["value"])
	if value == "" {
		writeError(w, http.StatusBadGateway, errors.New("Appium 未返回屏幕截图"))
		return
	}
	mime := "image/png"
	if r.URL.Query().Get("preview") == "1" {
		value, err = previewScreenshot(value)
		if err != nil {
			writeError(w, http.StatusBadGateway, fmt.Errorf("生成设备预览失败: %w", err))
			return
		}
		mime = "image/jpeg"
	}
	writeJSON(w, http.StatusOK, map[string]string{"image": value, "mime": mime})
}

func (s *server) handleStream(w http.ResponseWriter, r *http.Request) {
	udid, sessionID, ok := s.sessionOrError(w, r)
	if !ok {
		return
	}
	if s.isWDASession(udid) {
		if _, err := s.sessionRequest(r.Context(), udid, sessionID, http.MethodPost,
			sessionPath(sessionID)+"/appium/settings", map[string]any{
				"settings": map[string]int{
					"mjpegServerFramerate":         60,
					"mjpegServerScreenshotQuality": 25,
					"mjpegScalingFactor":           100,
				},
			}); err != nil {
			if errors.Is(err, errSessionLost) {
				s.writeAPIError(w, http.StatusBadGateway, err)
				return
			}
			// Older WDA builds do not expose /appium/settings. Keep streaming
			// with the device defaults instead of failing the whole stream.
			logDeviceEvent(r.Context(), "warn", udid, "wda_mjpeg_settings_failed", err)
		}
		endpoint, err := s.ensureWDAMjpegRelay(udid)
		if err != nil {
			writeError(w, http.StatusBadGateway, fmt.Errorf("连接 WDA 原生 MJPEG 视频服务失败: %w", err))
			return
		}
		s.proxyMJPEGStream(w, r, udid, endpoint)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("当前 HTTP 服务不支持视频流"))
		return
	}
	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=go-phone-frame")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("X-Accel-Buffering", "no")

	ticker := time.NewTicker(time.Second / time.Duration(runtimeCfg.AppiumStreamFPS))
	defer ticker.Stop()

	for {
		result, err := s.sessionRequest(r.Context(), udid, sessionID, http.MethodGet, sessionPath(sessionID)+"/screenshot", nil)
		if err != nil {
			logDeviceEvent(r.Context(), "warn", udid, "stream_screenshot_failed", err)
			return
		}
		encoded := stringValue(result["value"])
		if encoded == "" {
			logDeviceEvent(r.Context(), "warn", udid, "stream_empty_screenshot", nil)
			return
		}
		frame, err := encodeAppiumFrame(encoded)
		if err != nil {
			logDeviceEvent(r.Context(), "warn", udid, "stream_frame_failed", err)
			return
		}

		if _, err := fmt.Fprintf(w, "--go-phone-frame\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n",
			len(frame)); err != nil {
			return
		}
		if _, err := w.Write(frame); err != nil {
			return
		}
		if _, err := w.Write([]byte("\r\n")); err != nil {
			return
		}
		flusher.Flush()

		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *server) proxyMJPEGStream(w http.ResponseWriter, r *http.Request, udid, endpoint string) {
	transport := &http.Transport{
		DialContext: (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
	}
	defer transport.CloseIdleConnections()
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, strings.TrimRight(endpoint, "/")+"/", nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Errorf("创建 WDA 视频流请求失败: %w", err))
		return
	}
	response, err := (&http.Client{Transport: transport}).Do(request)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Errorf("读取 WDA 原生视频流失败: %w", err))
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		writeError(w, http.StatusBadGateway, fmt.Errorf("WDA 视频流返回 HTTP %d", response.StatusCode))
		return
	}
	contentType := response.Header.Get("Content-Type")
	if !strings.HasPrefix(strings.ToLower(contentType), "multipart/x-mixed-replace") {
		writeError(w, http.StatusBadGateway, fmt.Errorf("WDA 返回了非 MJPEG 视频流：%s", contentType))
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("当前 HTTP 服务不支持视频流"))
		return
	}
	w.WriteHeader(response.StatusCode)
	buffer := make([]byte, 16<<10)
	for {
		n, readErr := response.Body.Read(buffer)
		if n > 0 {
			if _, err := w.Write(buffer[:n]); err != nil {
				return
			}
			flusher.Flush()
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) && r.Context().Err() == nil {
				logDeviceEvent(r.Context(), "warn", udid, "wda_mjpeg_stream_interrupted", readErr)
			}
			return
		}
	}
}

func (s *server) handleTap(w http.ResponseWriter, r *http.Request) {
	var point tapInput
	if err := decodeJSON(r.Body, &point); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := validateImagePoint(point.X, point.Y, point.ImageWidth, point.ImageHeight); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	udid, sessionID, ok := s.sessionOrError(w, r)
	if !ok {
		return
	}
	if err := s.tapDevice(r.Context(), udid, sessionID, point); err != nil {
		s.writeAPIError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) handleSwipe(w http.ResponseWriter, r *http.Request) {
	var gesture swipeInput
	if err := decodeJSON(r.Body, &gesture); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := validateSwipeInput(gesture); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	udid, sessionID, ok := s.sessionOrError(w, r)
	if !ok {
		return
	}
	if err := s.swipeDevice(r.Context(), udid, sessionID, gesture); err != nil {
		s.writeAPIError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) handleType(w http.ResponseWriter, r *http.Request) {
	var input typeInput
	if err := decodeJSON(r.Body, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := validateTypeInput(input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	udid, sessionID, ok := s.sessionOrError(w, r)
	if !ok {
		return
	}
	if err := s.typeDevice(r.Context(), udid, sessionID, input); err != nil {
		s.writeAPIError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) handleAction(w http.ResponseWriter, r *http.Request) {
	var input actionInput
	if err := decodeJSON(r.Body, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := validateActionInput(input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	udid, sessionID, ok := s.sessionOrError(w, r)
	if !ok {
		return
	}
	if input.Action == "reboot" && s.isWDASession(udid) {
		writeErrorWithCode(w, http.StatusNotImplemented, CodeUnsupported, errors.New("当前 WDA 直连模式不支持重启，请使用独立的设备管理工具重启"))
		return
	}
	if err := s.actionDevice(r.Context(), udid, sessionID, input); err != nil {
		s.writeAPIError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) handleGetClipboard(w http.ResponseWriter, r *http.Request) {
	udid, sessionID, ok := s.sessionOrError(w, r)
	if !ok {
		return
	}
	endpoint := sessionPath(sessionID) + "/appium/device/get_clipboard"
	if s.isWDASession(udid) {
		endpoint = "/wda/getPasteboard"
	}
	result, err := s.sessionRequest(r.Context(), udid, sessionID, http.MethodGet, endpoint, nil)
	if err != nil {
		s.writeAPIError(w, http.StatusBadGateway, err)
		return
	}
	text, err := base64.StdEncoding.DecodeString(stringValue(result["value"]))
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Errorf("Appium 返回的剪贴板数据不是有效的 Base64: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"value": string(text)})
}

func (s *server) handleSetClipboard(w http.ResponseWriter, r *http.Request) {
	var input clipboardInput
	if err := decodeJSON(r.Body, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := validateClipboardInput(input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	udid, sessionID, ok := s.sessionOrError(w, r)
	if !ok {
		return
	}
	if err := s.setClipboardDevice(r.Context(), udid, sessionID, input); err != nil {
		s.writeAPIError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) handleMessages(w http.ResponseWriter, r *http.Request) {
	udid, sessionID, ok := s.sessionOrError(w, r)
	if !ok {
		return
	}
	count, appName, err := s.readUnreadCount(r.Context(), udid, sessionID)
	if err != nil {
		s.writeAPIError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"app": appName, "unread": count})
}

func (s *server) handleBatch(w http.ResponseWriter, r *http.Request) {
	var request batchRequest
	if err := decodeJSON(r.Body, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(request.UDIDs) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("批量操作至少需要一台设备"))
		return
	}
	if len(request.UDIDs) > runtimeCfg.MaxBatchDevices {
		writeError(w, http.StatusBadRequest, fmt.Errorf("批量操作最多支持 %d 台设备", runtimeCfg.MaxBatchDevices))
		return
	}
	seen := make(map[string]bool, len(request.UDIDs))
	for _, udid := range request.UDIDs {
		if err := validateUDID(udid); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if seen[udid] {
			writeError(w, http.StatusBadRequest, fmt.Errorf("设备 %s 在批量请求中重复出现", udid))
			return
		}
		seen[udid] = true
	}
	payload, err := parseBatchPayload(request.Operation, request.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	results := make([]batchResult, len(request.UDIDs))
	sem := make(chan struct{}, runtimeCfg.BatchConcurrency)
	requestCtx, cancelRequest := context.WithTimeout(r.Context(), runtimeCfg.BatchTotalTimeout)
	defer cancelRequest()
	var wait sync.WaitGroup
	for index, udid := range request.UDIDs {
		wait.Add(1)
		go func(index int, udid string) {
			defer wait.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			result := batchResult{UDID: udid, Name: s.deviceName(udid)}
			sessionID, err := s.sessionID(udid)
			if err != nil {
				result.Error = err.Error()
				result.Code = errorCode(err)
				results[index] = result
				return
			}
			ctx, cancel := context.WithTimeout(requestCtx, runtimeCfg.BatchDeviceTimeout)
			defer cancel()
			if err := s.performBatchOperation(ctx, udid, sessionID, request.Operation, payload); err != nil {
				result.Error = err.Error()
				result.Code = errorCode(err)
				results[index] = result
				return
			}
			result.OK = true
			results[index] = result
		}(index, udid)
	}
	wait.Wait()

	response := batchResponse{Total: len(results), Results: results}
	for _, result := range results {
		if result.OK {
			response.Succeeded++
		} else {
			response.Failed++
		}
	}
	response.OK = response.Failed == 0
	writeJSON(w, http.StatusOK, response)
}

func (s *server) sessionOrError(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	udid, err := requestUDID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return "", "", false
	}
	sessionID, err := s.sessionID(udid)
	if err != nil {
		s.writeAPIError(w, http.StatusConflict, err)
		return "", "", false
	}
	return udid, sessionID, true
}
