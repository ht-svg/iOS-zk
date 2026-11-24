package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

var muxTag uint32 = 1

type relaySet struct {
	endpoints map[string]string
	listeners map[string]net.Listener
	deviceIDs map[string]uint32
}

func (r *relaySet) init() {
	if r.endpoints == nil {
		r.endpoints = make(map[string]string)
	}
	if r.listeners == nil {
		r.listeners = make(map[string]net.Listener)
	}
	if r.deviceIDs == nil {
		r.deviceIDs = make(map[string]uint32)
	}
}

func (s *server) adoptRunningWDA(ctx context.Context, udid string) (string, string, error) {
	endpoint, err := s.ensureWDARelay(udid)
	if err != nil {
		return "", "", err
	}
	status, err := s.appiumRequest(ctx, endpoint, http.MethodGet, "/status", nil)
	if err != nil {
		return "", "", fmt.Errorf("无法连接手机上的 WebDriverAgent：%w", err)
	}
	value, _ := status["value"].(map[string]any)
	ready, _ := value["ready"].(bool)
	if !ready {
		return "", "", fmt.Errorf("手机上的 WebDriverAgent 尚未就绪")
	}
	sessionID := sessionIDFrom(status)
	if sessionID == "" {
		return "", "", fmt.Errorf("WebDriverAgent 已运行，但没有活动的自动化会话")
	}
	return sessionID, endpoint, nil
}

func (s *server) ensureWDARelay(udid string) (string, error) {
	return s.ensureWDAServiceRelay(udid, 8100, &s.wdaRelay)
}

func (s *server) ensureWDAMjpegRelay(udid string) (string, error) {
	return s.ensureWDAServiceRelay(udid, 9100, &s.wdaStreamRelay)
}

func (s *server) ensureWDAServiceRelay(udid string, port uint16, set *relaySet) (string, error) {
	devices, err := listUSBMuxDevices("127.0.0.1:27015")
	if err != nil {
		return "", err
	}
	var selected *muxDevice
	for i := range devices {
		if devices[i].UDID == udid {
			selected = &devices[i]
			break
		}
	}
	if selected == nil {
		s.closeRelay(udid, set)
		return "", fmt.Errorf("设备 %s 已不在 USB 列表中", udid)
	}

	s.mu.Lock()
	set.init()
	if endpoint := set.endpoints[udid]; endpoint != "" && set.deviceIDs[udid] == selected.DeviceID {
		s.mu.Unlock()
		return endpoint, nil
	}
	if listener := set.listeners[udid]; listener != nil {
		_ = listener.Close()
	}
	delete(set.endpoints, udid)
	delete(set.listeners, udid)
	delete(set.deviceIDs, udid)
	s.mu.Unlock()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("创建本机 WDA 转发失败: %w", err)
	}
	endpoint := "http://" + listener.Addr().String()

	s.mu.Lock()
	set.init()
	if existing := set.endpoints[udid]; existing != "" {
		if set.deviceIDs[udid] == selected.DeviceID {
			s.mu.Unlock()
			_ = listener.Close()
			return existing, nil
		}
		if oldListener := set.listeners[udid]; oldListener != nil {
			_ = oldListener.Close()
		}
	}
	set.endpoints[udid] = endpoint
	set.listeners[udid] = listener
	set.deviceIDs[udid] = selected.DeviceID
	s.mu.Unlock()

	go serveWDARelay(listener, selected.DeviceID, port)
	return endpoint, nil
}

func (s *server) closeRelay(udid string, set *relaySet) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeRelayLocked(udid, set)
}

// closeRelayLocked must be called with s.mu held.
func (s *server) closeRelayLocked(udid string, set *relaySet) {
	if set == nil {
		return
	}
	if listener := set.listeners[udid]; listener != nil {
		_ = listener.Close()
	}
	delete(set.endpoints, udid)
	delete(set.listeners, udid)
	delete(set.deviceIDs, udid)
}

func serveWDARelay(listener net.Listener, deviceID uint32, port uint16) {
	for {
		client, err := listener.Accept()
		if err != nil {
			return
		}
		go relayWDAConnection(client, deviceID, port)
	}
}

func relayWDAConnection(client net.Conn, deviceID uint32, port uint16) {
	defer client.Close()
	device, err := connectUSBMuxService(deviceID, port)
	if err != nil {
		return
	}
	defer device.Close()

	// Either direction ending must tear down both sides. Waiting for the
	// client->device copy first leaks a goroutine on every keep-alive request.
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(device, client)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, device)
		done <- struct{}{}
	}()
	<-done
	_ = client.Close()
	_ = device.Close()
	<-done
}

func connectUSBMuxService(deviceID uint32, port uint16) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:27015", 3*time.Second)
	if err != nil {
		return nil, fmt.Errorf("连接 usbmuxd: %w", err)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	portNumber := binary.BigEndian.Uint16([]byte{byte(port), byte(port >> 8)})
	payload := []byte(fmt.Sprintf(
		`<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>ClientVersionString</key><string>libusbmuxd 1.1.0</string><key>MessageType</key><string>Connect</string><key>ProgName</key><string>GoPhoneManager</string><key>DeviceID</key><integer>%d</integer><key>PortNumber</key><integer>%d</integer><key>kLibUSBMuxVersion</key><integer>3</integer></dict></plist>`,
		deviceID, portNumber,
	))
	packet := make([]byte, 16+len(payload))
	binary.LittleEndian.PutUint32(packet[:4], uint32(len(packet)))
	binary.LittleEndian.PutUint32(packet[4:8], 1)
	binary.LittleEndian.PutUint32(packet[8:12], 8)
	tag := atomic.AddUint32(&muxTag, 1)
	binary.LittleEndian.PutUint32(packet[12:16], tag)
	copy(packet[16:], payload)
	if err := writeAll(conn, packet); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("发送 usbmuxd Connect 请求: %w", err)
	}
	header := make([]byte, 16)
	if _, err := io.ReadFull(conn, header); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("读取 usbmuxd Connect 响应: %w", err)
	}
	length := binary.LittleEndian.Uint32(header[:4])
	if length < 16 || length > maxMuxPacket {
		_ = conn.Close()
		return nil, fmt.Errorf("usbmuxd Connect 响应长度异常: %d", length)
	}
	response := make([]byte, length-16)
	if _, err := io.ReadFull(conn, response); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("读取 usbmuxd Connect 响应内容: %w", err)
	}
	root, err := decodeRootPlist(xml.NewDecoder(bytes.NewReader(response)))
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if number := root.dict["Number"].text; number != "" && number != "0" {
		_ = conn.Close()
		return nil, fmt.Errorf("usbmuxd 无法连接设备服务端口 %d（错误码 %s）", port, number)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
