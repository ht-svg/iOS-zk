package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The backend signs every "code|device" binding with an Ed25519 key. The client
// embeds only the public key, so a forged 授权备份.json cannot be verified even
// when the attacker has full control of the files on disk.
type signer struct {
	private ed25519.PrivateKey
	public  ed25519.PublicKey
}

func loadOrCreateSigner(path string) (*signer, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		raw, decodeErr := hex.DecodeString(strings.TrimSpace(string(data)))
		if decodeErr != nil || len(raw) != ed25519.PrivateKeySize {
			return nil, fmt.Errorf("签名密钥文件格式不正确：%s（应为 %d 字节十六进制）", path, ed25519.PrivateKeySize)
		}
		private := ed25519.PrivateKey(raw)
		return &signer{private: private, public: private.Public().(ed25519.PublicKey)}, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(private)), 0o600); err != nil {
		return nil, fmt.Errorf("写入签名密钥失败：%w", err)
	}
	return &signer{private: private, public: public}, nil
}

// canonicalExpiry is the single textual form of an expiry: RFC3339 in UTC with
// no fractional seconds. Signing, /api/v1/bind and /api/v1/status all use it so
// the client can rebuild the exact signed byte string.
func canonicalExpiry(at *time.Time) string {
	if at == nil {
		return ""
	}
	return at.UTC().Truncate(time.Second).Format(time.RFC3339)
}

// bindingMessage is the exact byte string that gets signed. The client builds
// the same string, so any change here must be mirrored in license_client.go.
// The expiry is part of the payload so it cannot be extended offline.
func bindingMessage(code, udid string, expiresAt *time.Time) []byte {
	return []byte(strings.ToUpper(strings.TrimSpace(code)) + "|" + strings.TrimSpace(udid) + "|" + canonicalExpiry(expiresAt))
}

func (s *signer) signBinding(code, udid string, expiresAt *time.Time) string {
	if s == nil {
		return ""
	}
	return hex.EncodeToString(ed25519.Sign(s.private, bindingMessage(code, udid, expiresAt)))
}

func (s *signer) publicHex() string {
	if s == nil {
		return ""
	}
	return hex.EncodeToString(s.public)
}
