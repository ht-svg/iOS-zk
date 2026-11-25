package main

import (
	"testing"
	"time"
)

func TestLoadRuntimeConfigEnvOverrides(t *testing.T) {
	t.Setenv("GO_PHONE_MANAGER_LISTEN", "127.0.0.1:9999")
	t.Setenv("GO_PHONE_MANAGER_WDA_PORT", "8800")
	t.Setenv("GO_PHONE_MANAGER_BATCH_CONCURRENCY", "4")
	t.Setenv("GO_PHONE_MANAGER_HTTP_TIMEOUT", "12")

	cfg := loadRuntimeConfig()
	if cfg.ListenAddress != "127.0.0.1:9999" {
		t.Fatalf("listen address = %q", cfg.ListenAddress)
	}
	if cfg.WDAPort != 8800 {
		t.Fatalf("WDA port = %d, want 8800", cfg.WDAPort)
	}
	if cfg.BatchConcurrency != 4 {
		t.Fatalf("batch concurrency = %d, want 4", cfg.BatchConcurrency)
	}
	if cfg.HTTPTimeout != 12*time.Second {
		t.Fatalf("HTTP timeout = %s, want 12s", cfg.HTTPTimeout)
	}
}

func TestLoadRuntimeConfigAvoidsPortCollision(t *testing.T) {
	t.Setenv("GO_PHONE_MANAGER_WDA_PORT", "9100")
	t.Setenv("GO_PHONE_MANAGER_WDA_STREAM_PORT", "9100")

	cfg := loadRuntimeConfig()
	if cfg.WDAPort == cfg.WDAStreamPort {
		t.Fatalf("WDA and stream ports both = %d", cfg.WDAPort)
	}
}

func TestLoadRuntimeConfigRejectsInvalidValues(t *testing.T) {
	t.Setenv("GO_PHONE_MANAGER_WDA_PORT", "70000")
	t.Setenv("GO_PHONE_MANAGER_BATCH_CONCURRENCY", "0")

	cfg := loadRuntimeConfig()
	if cfg.WDAPort != 8100 {
		t.Fatalf("invalid WDA port should fall back to 8100, got %d", cfg.WDAPort)
	}
	if cfg.BatchConcurrency != 50 {
		t.Fatalf("invalid concurrency should fall back to 50, got %d", cfg.BatchConcurrency)
	}
}
