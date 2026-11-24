package main

import (
	"path/filepath"
	"testing"
)

func TestSaveConfigCanReplaceExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	s := &server{
		configPath: path,
		config: Config{
			AppiumURLs: map[string]string{"device-udid": "http://127.0.0.1:4723"},
			Names:      map[string]string{"device-udid": "测试设备"},
			Groups:     map[string]string{"device-udid": "一组"},
		},
	}
	if err := s.saveConfigLocked(); err != nil {
		t.Fatal(err)
	}
	s.config.Names["device-udid"] = "更新后的设备名"
	if err := s.saveConfigLocked(); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Names["device-udid"] != "更新后的设备名" || cfg.Groups["device-udid"] != "一组" {
		t.Fatalf("unexpected saved config: %+v", cfg)
	}
}
