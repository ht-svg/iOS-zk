package main

// Build metadata. Override at build time:
//
//	go build -ldflags "-X main.appVersion=0.1.0 -X main.appBuildTime=2026-11-26T01:00:00Z"
var (
	appVersion   = "0.1.0"
	appBuildTime = "dev"
)

func versionString() string {
	return appVersion + " (build " + appBuildTime + ")"
}
