package main

import (
	"os"
	"strconv"
	"time"
)

// runtimeConfig holds process-wide settings that were previously scattered
// as literals across handlers, relays and usbmux code. Values can be
// overridden with environment variables before starting the executable.
type runtimeConfig struct {
	ListenAddress        string
	USBMuxAddress        string
	DefaultAppiumURL     string
	WDAPort              uint16
	WDAStreamPort        uint16
	HTTPTimeout          time.Duration
	USBMuxDialTimeout    time.Duration
	USBMuxRequestTimeout time.Duration
	RelayDialTimeout     time.Duration
	ProbeTimeout         time.Duration
	WindowSizeTTL        time.Duration
	BatchTotalTimeout    time.Duration
	BatchDeviceTimeout   time.Duration
	BatchConcurrency     int
	MaxBatchDevices      int
	AppiumStreamFPS      int
	ConfigDirName        string
	ConfigFileName       string
	ClientName           string
}

const runtimeEnvPrefix = "GO_PHONE_MANAGER_"

var runtimeCfg = loadRuntimeConfig()

func loadRuntimeConfig() runtimeConfig {
	cfg := runtimeConfig{
		ListenAddress:        envString("LISTEN", "127.0.0.1:8787"),
		USBMuxAddress:        envString("USB_MUX", "127.0.0.1:27015"),
		DefaultAppiumURL:     envString("APPIUM_URL", "http://127.0.0.1:4723"),
		WDAPort:              envUint16("WDA_PORT", 8100, 1, 65535),
		WDAStreamPort:        envUint16("WDA_STREAM_PORT", 9100, 1, 65535),
		HTTPTimeout:          envSeconds("HTTP_TIMEOUT", 45, 1, 600),
		USBMuxDialTimeout:    envSeconds("USBMUX_DIAL_TIMEOUT", 2, 1, 60),
		USBMuxRequestTimeout: envSeconds("USBMUX_REQUEST_TIMEOUT", 5, 1, 60),
		RelayDialTimeout:     envSeconds("RELAY_DIAL_TIMEOUT", 3, 1, 60),
		ProbeTimeout:         envSeconds("PROBE_TIMEOUT", 5, 1, 60),
		WindowSizeTTL:        envSeconds("WINDOW_SIZE_TTL", 10, 1, 3600),
		BatchTotalTimeout:    envSeconds("BATCH_TOTAL_TIMEOUT", 60, 1, 3600),
		BatchDeviceTimeout:   envSeconds("BATCH_DEVICE_TIMEOUT", 30, 1, 3600),
		BatchConcurrency:     envInt("BATCH_CONCURRENCY", 8, 1, 64),
		MaxBatchDevices:      envInt("MAX_BATCH_DEVICES", 128, 1, 1024),
		AppiumStreamFPS:      envInt("APPIUM_STREAM_FPS", 15, 1, 60),
		ConfigDirName:        envString("CONFIG_DIR", "GoPhoneManager"),
		ConfigFileName:       envString("CONFIG_FILE", "config.json"),
		ClientName:           envString("CLIENT_NAME", "GoPhoneManager"),
	}
	if cfg.WDAPort == cfg.WDAStreamPort {
		if cfg.WDAStreamPort < 65535 {
			cfg.WDAStreamPort++
		} else {
			cfg.WDAStreamPort--
		}
	}
	return cfg
}

func envString(name, fallback string) string {
	if value := os.Getenv(runtimeEnvPrefix + name); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback, min, max int) int {
	value, err := strconv.Atoi(os.Getenv(runtimeEnvPrefix + name))
	if err != nil || value < min || value > max {
		return fallback
	}
	return value
}

func envUint16(name string, fallback uint16, min, max uint16) uint16 {
	value, err := strconv.ParseUint(os.Getenv(runtimeEnvPrefix+name), 10, 16)
	if err != nil || uint16(value) < min || uint16(value) > max {
		return fallback
	}
	return uint16(value)
}

func envSeconds(name string, fallback, min, max int) time.Duration {
	return time.Duration(envInt(name, fallback, min, max)) * time.Second
}
