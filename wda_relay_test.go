package main

import (
	"strconv"
	"strings"
	"testing"
)

func TestUSBMuxConnectPayloadUsesNetworkOrderedPort(t *testing.T) {
	cases := []struct {
		port uint16
		want uint16
	}{
		{8100, 42015},
		{9100, 35875},
	}
	for _, tc := range cases {
		payload := string(usbmuxConnectPayload("GoPhoneManager", 7, tc.port))
		want := "<key>PortNumber</key><integer>" + strconv.Itoa(int(tc.want)) + "</integer>"
		if !strings.Contains(payload, want) {
			t.Fatalf("port %d: payload does not contain %q: %s", tc.port, want, payload)
		}
		raw := "<integer>" + strconv.Itoa(int(tc.port)) + "</integer>"
		if strings.Contains(payload, raw) {
			t.Fatalf("port %d: payload contains unswapped port: %s", tc.port, payload)
		}
	}
}
