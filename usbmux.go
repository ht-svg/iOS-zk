package main

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

const maxMuxPacket = 4 << 20

type muxDevice struct {
	DeviceID       uint32
	UDID           string
	ConnectionType string
}

type plistValue struct {
	text  string
	dict  map[string]plistValue
	array []plistValue
}

func listUSBMuxDevices(address string) ([]muxDevice, error) {
	conn, err := net.DialTimeout("tcp", address, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("无法连接 Windows usbmuxd（%s）：%w。请安装 Apple 设备支持组件、连接并信任 iPhone", address, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	payload := []byte(`<?xml version="1.0" encoding="UTF-8"?>` +
		`<plist version="1.0"><dict>` +
		`<key>ClientVersionString</key><string>libusbmuxd 1.1.0</string>` +
		`<key>MessageType</key><string>ListDevices</string>` +
		`<key>ProgName</key><string>GoPhoneManager</string>` +
		`<key>kLibUSBMuxVersion</key><integer>3</integer>` +
		`</dict></plist>`)
	packet := make([]byte, 16+len(payload))
	binary.LittleEndian.PutUint32(packet[0:4], uint32(len(packet)))
	binary.LittleEndian.PutUint32(packet[4:8], 1)
	binary.LittleEndian.PutUint32(packet[8:12], 8)
	binary.LittleEndian.PutUint32(packet[12:16], 1)
	copy(packet[16:], payload)
	if _, err := conn.Write(packet); err != nil {
		return nil, fmt.Errorf("向 usbmuxd 发送请求失败: %w", err)
	}

	header := make([]byte, 16)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, fmt.Errorf("读取 usbmuxd 响应失败: %w", err)
	}
	packetLength := binary.LittleEndian.Uint32(header[:4])
	if packetLength < 16 || packetLength > maxMuxPacket {
		return nil, fmt.Errorf("usbmuxd 响应长度异常: %d", packetLength)
	}
	response := make([]byte, packetLength-16)
	if _, err := io.ReadFull(conn, response); err != nil {
		return nil, fmt.Errorf("读取 usbmuxd 设备列表失败: %w", err)
	}
	return parseUSBMuxDevices(response)
}

func parseUSBMuxDevices(data []byte) ([]muxDevice, error) {
	root, err := decodeRootPlist(xml.NewDecoder(bytes.NewReader(data)))
	if err != nil {
		return nil, err
	}
	if number := root.dict["Number"].text; number != "" && number != "0" {
		return nil, fmt.Errorf("usbmuxd 返回错误码 %s", number)
	}
	list := root.dict["DeviceList"].array
	devices := make([]muxDevice, 0, len(list))
	seen := make(map[string]bool)
	for _, item := range list {
		properties := item.dict["Properties"].dict
		udid := properties["SerialNumber"].text
		if udid == "" {
			udid = properties["UDID"].text
		}
		if udid == "" || seen[udid] {
			continue
		}
		seen[udid] = true
		connection := strings.ToLower(properties["ConnectionType"].text)
		deviceID, _ := strconv.ParseUint(properties["DeviceID"].text, 10, 32)
		devices = append(devices, muxDevice{DeviceID: uint32(deviceID), UDID: udid, ConnectionType: connection})
	}
	return devices, nil
}

func decodeRootPlist(decoder *xml.Decoder) (plistValue, error) {
	for {
		token, err := decoder.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return plistValue{}, errors.New("usbmuxd 返回空 plist")
			}
			return plistValue{}, fmt.Errorf("读取 usbmuxd plist: %w", err)
		}
		if start, ok := token.(xml.StartElement); ok && start.Name.Local == "plist" {
			for {
				token, err = decoder.Token()
				if err != nil {
					return plistValue{}, fmt.Errorf("读取 usbmuxd plist: %w", err)
				}
				if start, ok = token.(xml.StartElement); ok {
					return decodePlistValue(decoder, start)
				}
			}
		}
	}
}

func decodePlistValue(decoder *xml.Decoder, start xml.StartElement) (plistValue, error) {
	value := plistValue{}
	switch start.Name.Local {
	case "dict":
		value.dict = make(map[string]plistValue)
		for {
			token, err := decoder.Token()
			if err != nil {
				return value, err
			}
			switch token := token.(type) {
			case xml.EndElement:
				if token.Name.Local == "dict" {
					return value, nil
				}
			case xml.StartElement:
				if token.Name.Local != "key" {
					return value, fmt.Errorf("usbmuxd plist 中预期 key，实际为 %s", token.Name.Local)
				}
				var key string
				if err := decoder.DecodeElement(&key, &token); err != nil {
					return value, err
				}
				child, err := nextPlistStart(decoder)
				if err != nil {
					return value, err
				}
				parsed, err := decodePlistValue(decoder, child)
				if err != nil {
					return value, err
				}
				value.dict[key] = parsed
			}
		}

	case "array":
		for {
			token, err := decoder.Token()
			if err != nil {
				return value, err
			}
			switch token := token.(type) {
			case xml.EndElement:
				if token.Name.Local == "array" {
					return value, nil
				}
			case xml.StartElement:
				item, err := decodePlistValue(decoder, token)
				if err != nil {
					return value, err
				}
				value.array = append(value.array, item)
			}
		}
	case "string", "integer", "real", "date":
		if err := decoder.DecodeElement(&value.text, &start); err != nil {
			return value, err
		}
		return value, nil
	case "true", "false":
		value.text = start.Name.Local
		if err := decoder.DecodeElement(new(string), &start); err != nil {
			return value, err
		}
		return value, nil
	default:
		return value, fmt.Errorf("usbmuxd plist 类型不支持: %s", start.Name.Local)
	}
}

func nextPlistStart(decoder *xml.Decoder) (xml.StartElement, error) {
	for {
		token, err := decoder.Token()
		if err != nil {
			return xml.StartElement{}, err
		}
		switch token := token.(type) {
		case xml.StartElement:
			return token, nil
		case xml.CharData:
			if strings.TrimSpace(string(token)) != "" {
				return xml.StartElement{}, errors.New("usbmuxd plist 字典值格式无效")
			}
		case xml.EndElement:
			return xml.StartElement{}, errors.New("usbmuxd plist 字典值缺失")
		}
	}
}
