package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	AppiumURLs    map[string]string `json:"appiumUrls"`
	Names         map[string]string `json:"names"`
	Groups        map[string]string `json:"groups"`
	LicenseServer string            `json:"licenseServer,omitempty"`
	LicenseToken  string            `json:"licenseToken,omitempty"`
}

type Device struct {
	UDID           string `json:"udid"`
	ConnectionType string `json:"connectionType"`
	Name           string `json:"name"`
	Group          string `json:"group"`
	AppiumURL      string `json:"appiumUrl"`
	SessionID      string `json:"sessionId,omitempty"`
	SessionType    string `json:"sessionType,omitempty"`
	Authorized     bool   `json:"authorized"`
	LicenseCode    string `json:"licenseCode,omitempty"`
	LicenseReason  string `json:"licenseReason,omitempty"`
}

func defaultConfigPath() (string, error) {
	root := os.Getenv("LOCALAPPDATA")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("find user config directory: %w", err)
		}
		root = filepath.Join(home, "AppData", "Local")
	}
	dir := filepath.Join(root, runtimeCfg.ConfigDirName)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create config directory: %w", err)
	}
	return filepath.Join(dir, runtimeCfg.ConfigFileName), nil
}

func loadConfig(path string) (Config, error) {
	cfg := Config{
		AppiumURLs: make(map[string]string),
		Names:      make(map[string]string),
		Groups:     make(map[string]string),
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	if cfg.AppiumURLs == nil {
		cfg.AppiumURLs = make(map[string]string)
	}
	if cfg.Names == nil {
		cfg.Names = make(map[string]string)
	}
	if cfg.Groups == nil {
		cfg.Groups = make(map[string]string)
	}
	// The old UDID-derived licence map is obsolete. Strip it so it cannot leak
	// stale authorisation data or confuse support.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err == nil {
		if _, stale := raw["licenses"]; stale {
			delete(raw, "licenses")
			if cleaned, err := json.MarshalIndent(raw, "", "  "); err == nil {
				_ = os.WriteFile(path, cleaned, 0600)
			}
		}
	}
	return cfg, nil
}

func (s *server) saveConfigLocked() error {
	data, err := json.MarshalIndent(s.config, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	tmp := s.configPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Rename(tmp, s.configPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}

func (s *server) handleDevices(w http.ResponseWriter, r *http.Request) {
	found, err := listUSBMuxDevices(runtimeCfg.USBMuxAddress)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	s.reconcileDevices(found)

	s.mu.RLock()
	defer s.mu.RUnlock()
	devices := make([]Device, 0, len(found))
	for _, d := range found {
		name := s.config.Names[d.UDID]
		if name == "" {
			name = "iPhone"
		}
		appiumURL := s.config.AppiumURLs[d.UDID]
		if appiumURL == "" {
			appiumURL = runtimeCfg.DefaultAppiumURL
		}
		licenseState := s.license.Devices[d.UDID]
		devices = append(devices, Device{
			UDID:           d.UDID,
			ConnectionType: d.ConnectionType,
			Name:           name,
			Group:          s.config.Groups[d.UDID],
			AppiumURL:      appiumURL,
			SessionID:      s.sessions[d.UDID],
			SessionType:    sessionType(s.wdaSessions[d.UDID]),
			Authorized:     licenseState.Authorized,
			LicenseCode:    licenseState.Code,
			LicenseReason:  licenseState.Reason,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": devices})
}

func (s *server) handleSettings(w http.ResponseWriter, r *http.Request) {
	udid, err := requestUDID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var body struct {
		Name      string `json:"name"`
		Group     string `json:"group"`
		AppiumURL string `json:"appiumUrl"`
	}
	if err := decodeJSON(r.Body, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	body.Group = strings.TrimSpace(body.Group)
	if body.Name != "" && len(body.Name) > 80 {
		writeError(w, http.StatusBadRequest, errors.New("设备名称不能超过 80 个字符"))
		return
	}
	if len(body.Group) > 80 {
		writeError(w, http.StatusBadRequest, errors.New("分组名称不能超过 80 个字符"))
		return
	}
	if body.AppiumURL != "" {
		if err := validateLocalURL(body.AppiumURL); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}

	s.mu.Lock()
	if body.Name == "" {
		delete(s.config.Names, udid)
	} else {
		s.config.Names[udid] = body.Name
	}
	if body.Group == "" {
		delete(s.config.Groups, udid)
	} else {
		s.config.Groups[udid] = body.Group
	}
	if body.AppiumURL == "" {
		delete(s.config.AppiumURLs, udid)
	} else {
		s.config.AppiumURLs[udid] = strings.TrimRight(body.AppiumURL, "/")
	}
	err = s.saveConfigLocked()
	s.mu.Unlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) deviceName(udid string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if name := s.config.Names[udid]; name != "" {
		return name
	}
	return "iPhone"
}
