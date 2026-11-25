package main

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseStreamOptionsDefaultsToZero(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/devices/x/stream", nil)
	got := parseStreamOptions(request)
	if got.fps != 0 || got.quality != 0 || got.scale != 0 {
		t.Fatalf("parseStreamOptions = %+v, want all zero when no query is set", got)
	}
}

func TestParseStreamOptionsReadsQuery(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/devices/x/stream?fps=24&quality=50&scale=75", nil)
	got := parseStreamOptions(request)
	if got.fps != 24 || got.quality != 50 || got.scale != 75 {
		t.Fatalf("parseStreamOptions = %+v, want 24/50/75", got)
	}
}

func TestParseStreamOptionsClampsOutOfRange(t *testing.T) {
	cases := []struct {
		query string
		want  streamOptions
	}{
		{"?fps=999&quality=999&scale=999", streamOptions{fps: 60, quality: 100, scale: 100}},
		{"?fps=0&quality=0&scale=0", streamOptions{fps: 1, quality: 1, scale: 25}},
		{"?fps=-5&quality=-5&scale=-5", streamOptions{fps: 1, quality: 1, scale: 25}},
	}
	for _, tc := range cases {
		request := httptest.NewRequest(http.MethodGet, "/api/devices/x/stream"+tc.query, nil)
		if got := parseStreamOptions(request); got != tc.want {
			t.Fatalf("%s: parseStreamOptions = %+v, want %+v", tc.query, got, tc.want)
		}
	}
}

func TestParseStreamOptionsIgnoresGarbage(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/devices/x/stream?fps=abc&quality=&scale=12x", nil)
	got := parseStreamOptions(request)
	if got.fps != 0 || got.quality != 0 || got.scale != 0 {
		t.Fatalf("parseStreamOptions = %+v, want zero for unparsable values", got)
	}
}

func TestStreamOptionsWithDefaults(t *testing.T) {
	empty := streamOptions{}.withDefaults(60, 25, 100)
	if empty != (streamOptions{fps: 60, quality: 25, scale: 100}) {
		t.Fatalf("empty options = %+v, want the mode defaults", empty)
	}
	custom := streamOptions{fps: 15}.withDefaults(60, 25, 100)
	if custom != (streamOptions{fps: 15, quality: 25, scale: 100}) {
		t.Fatalf("partial options = %+v, want only fps overridden", custom)
	}
}

func encodeTestScreenshot(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 40, 80))
	for y := 0; y < 80; y++ {
		for x := 0; x < 40; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 6), G: uint8(y * 3), B: 120, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestEncodeAppiumFrameScalesWidth(t *testing.T) {
	encoded := encodeTestScreenshot(t)

	full, err := encodeAppiumFrame(encoded, 75, 100)
	if err != nil {
		t.Fatalf("encodeAppiumFrame(100%%) error: %v", err)
	}
	half, err := encodeAppiumFrame(encoded, 75, 50)
	if err != nil {
		t.Fatalf("encodeAppiumFrame(50%%) error: %v", err)
	}

	fullImg, _, err := image.Decode(bytes.NewReader(full))
	if err != nil {
		t.Fatal(err)
	}
	halfImg, _, err := image.Decode(bytes.NewReader(half))
	if err != nil {
		t.Fatal(err)
	}
	if fullImg.Bounds().Dx() != 40 || fullImg.Bounds().Dy() != 80 {
		t.Fatalf("100%% frame = %v, want 40x80", fullImg.Bounds())
	}
	if halfImg.Bounds().Dx() != 20 || halfImg.Bounds().Dy() != 40 {
		t.Fatalf("50%% frame = %v, want 20x40", halfImg.Bounds())
	}
}

func TestEncodeAppiumFrameFallsBackToDefaultQuality(t *testing.T) {
	encoded := encodeTestScreenshot(t)
	if _, err := encodeAppiumFrame(encoded, 0, 100); err != nil {
		t.Fatalf("encodeAppiumFrame with quality 0 should fall back, got %v", err)
	}
	if _, err := encodeAppiumFrame(encoded, 200, 100); err != nil {
		t.Fatalf("encodeAppiumFrame with quality 200 should fall back, got %v", err)
	}
}
