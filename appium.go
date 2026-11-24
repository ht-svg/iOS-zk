package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

func base64String(value string) string {
	return base64.StdEncoding.EncodeToString([]byte(value))
}

func messageTarget(appName string) (xpath, attribute, mode string) {
	switch appName {
	case "WhatsApp":
		return `//XCUIElementTypeButton[@index="3"]`, "value", "whatsapp"
	case "WeChat", "微信":
		return `//XCUIElementTypeNavigationBar[@index="0"]`, "name", "digits"
	case "Messages", "信息":
		return `//XCUIElementTypeCollectionView[@index="0"]`, "value", "messages"
	case "抖音":
		return `//XCUIElementTypeButton[@index="7"]`, "name", "tiktok"
	case "小红书":
		return `//XCUIElementTypeButton[@index="3"]`, "value", "digits"
	case "闲鱼":
		return `//XCUIElementTypeButton[@index="3"]`, "name", "digits"
	default:
		return "", "", ""
	}
}

var firstDigits = regexp.MustCompile(`\d+`)

func firstNumber(value string) int {
	match := firstDigits.FindString(value)
	if match == "" {
		return 0
	}
	n, err := strconv.Atoi(match)
	if err != nil {
		return 0
	}
	return n
}

func (s *server) appiumRequest(ctx context.Context, baseURL, method, endpoint string, body any) (map[string]any, error) {
	if err := validateLocalURL(baseURL); err != nil {
		return nil, err
	}
	target := strings.TrimRight(baseURL, "/") + endpoint
	var requestBody *strings.Reader
	if body == nil {
		requestBody = strings.NewReader("")
	} else {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode Appium request: %w", err)
		}
		requestBody = strings.NewReader(string(data))
	}
	request, err := http.NewRequestWithContext(ctx, method, target, requestBody)
	if err != nil {
		return nil, fmt.Errorf("create Appium request: %w", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := s.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("连接 Appium %s 失败: %w", baseURL, err)
	}
	defer response.Body.Close()
	var result map[string]any
	decoder := json.NewDecoder(io.LimitReader(response.Body, 16<<20))
	if err := decoder.Decode(&result); err != nil {
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, fmt.Errorf("Appium 返回 HTTP %d", response.StatusCode)
		}
		return nil, fmt.Errorf("解析 Appium 响应失败: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := appiumErrorMessage(result)
		if message == "" {
			message = fmt.Sprintf("HTTP %d", response.StatusCode)
		}
		if len(message) > 500 {
			message = message[:500]
		}
		return nil, fmt.Errorf("Appium 返回错误: %s", message)
	}
	if value, ok := result["value"].(map[string]any); ok && stringValue(value["error"]) != "" {
		return nil, fmt.Errorf("Appium: %s", appiumErrorMessage(result))
	}
	return result, nil
}

func (s *server) readUnreadCount(ctx context.Context, udid, sessionID string) (int, string, error) {
	root, err := s.sessionRequest(ctx, udid, sessionID, http.MethodPost, sessionPath(sessionID)+"/element", map[string]string{
		"using": "xpath", "value": `//XCUIElementTypeApplication[@index="0"]`,
	})
	if err != nil {
		if isMissingElement(err) {
			return 0, "", nil
		}
		return 0, "", err
	}
	rootID := elementIDFrom(root)
	if rootID == "" {
		return 0, "", fmt.Errorf("Appium 响应中缺少应用元素 ID")
	}
	appName, err := s.elementAttributeContext(ctx, udid, sessionID, rootID, "name")
	if err != nil {
		return 0, "", err
	}
	appName = strings.TrimSpace(strings.ReplaceAll(appName, "\u200e", ""))
	xpath, attribute, mode := messageTarget(appName)
	if xpath == "" {
		return 0, appName, nil
	}

	var elementID string
	if mode == "messages" {
		collection, err := s.sessionRequest(ctx, udid, sessionID, http.MethodPost, sessionPath(sessionID)+"/element", map[string]string{
			"using": "xpath", "value": xpath,
		})
		if err != nil {
			if isMissingElement(err) {
				return 0, appName, nil
			}
			return 0, appName, err
		}
		collectionID := elementIDFrom(collection)
		if collectionID == "" {
			return 0, appName, nil
		}
		cells, err := s.sessionRequest(ctx, udid, sessionID, http.MethodPost, sessionPath(sessionID)+"/element/"+url.PathEscape(collectionID)+"/elements", map[string]string{
			"using": "xpath", "value": ".//XCUIElementTypeCell",
		})
		if err != nil {
			if isMissingElement(err) {
				return 0, appName, nil
			}
			return 0, appName, err
		}
		items, _ := cells["value"].([]any)
		index := 3
		if len(items) == 4 {
			index = 1
		}
		if len(items) <= index {
			return 0, appName, nil
		}
		elementID = elementIDFrom(map[string]any{"value": items[index]})
	} else {
		element, err := s.sessionRequest(ctx, udid, sessionID, http.MethodPost, sessionPath(sessionID)+"/element", map[string]string{
			"using": "xpath", "value": xpath,
		})
		if err != nil && mode != "tiktok" && !isMissingElement(err) {
			return 0, appName, err
		}
		elementID = elementIDFrom(element)
		if elementID == "" && mode == "tiktok" {
			element, err = s.sessionRequest(ctx, udid, sessionID, http.MethodPost, sessionPath(sessionID)+"/element", map[string]string{
				"using": "xpath", "value": `//XCUIElementTypeButton[@index="8"]`,
			})
			if err != nil && !isMissingElement(err) {
				return 0, appName, err
			}
			elementID = elementIDFrom(element)
		}
	}
	if elementID == "" {
		return 0, appName, nil
	}
	value, err := s.elementAttributeContext(ctx, udid, sessionID, elementID, attribute)
	if err != nil {
		return 0, appName, err
	}
	if mode == "whatsapp" && (value == "" || value == "1") {
		return 0, appName, nil
	}
	return firstNumber(value), appName, nil
}

func isMissingElement(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no such element")
}

func (s *server) elementAttributeContext(ctx context.Context, udid, sessionID, elementID, attribute string) (string, error) {
	result, err := s.sessionRequest(ctx, udid, sessionID, http.MethodGet,
		sessionPath(sessionID)+"/element/"+url.PathEscape(elementID)+"/attribute/"+url.PathEscape(attribute), nil)
	if err != nil {
		return "", err
	}
	return stringValue(result["value"]), nil
}
