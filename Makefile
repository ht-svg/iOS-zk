GO ?= go
GOLANGCI_LINT ?= golangci-lint
COVERAGE_MIN ?= 35.0

VERSION ?= 0.1.0
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.appVersion=$(VERSION) -X main.appBuildTime=$(BUILD_TIME)
APP := 海涛中控v$(VERSION).exe

.PHONY: fmt fmt-check vet lint test race cover cover-check check check-full build release sign

fmt:
	$(GO) fmt ./...

fmt-check:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "以下文件需要 gofmt："; \
		echo "$$unformatted"; \
		exit 1; \
	fi

vet:
	$(GO) vet ./...

lint:
	@command -v $(GOLANGCI_LINT) >/dev/null 2>&1 || { echo "未安装 golangci-lint：$(GOLANGCI_LINT)"; exit 1; }
	$(GOLANGCI_LINT) run ./...

test:
	$(GO) test -count=1 ./...

race:
	CGO_ENABLED=1 $(GO) test -race -count=1 ./...

cover:
	$(GO) test -count=1 -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -1

cover-check: cover
	@cov="$$($(GO) tool cover -func=coverage.out | tail -1 | awk '{print $$3}' | tr -d '%')"; \
	echo "覆盖率 $$cov%（最低 $(COVERAGE_MIN)%）"; \
	awk -v cov="$$cov" -v min="$(COVERAGE_MIN)" 'BEGIN { exit (cov < min) }'

check: fmt-check vet test

check-full: check lint race cover-check

build:
	$(GO) build -ldflags "$(LDFLAGS)" -o "$(APP)" .

# 正式发版：先跑完整检查，再产出带版本号的 exe 与压缩包
release: check
	$(GO) build -ldflags "$(LDFLAGS)" -o "$(APP)" .
	@echo "产物：$(APP)（版本 $(VERSION)，构建于 $(BUILD_TIME)）"

# 代码签名（需要先准备证书，未配置时只提示）
sign:
	@command -v signtool >/dev/null 2>&1 || { echo "未找到 signtool，请安装 Windows SDK"; exit 1; }
	signtool sign /fd SHA256 /tr http://timestamp.digicert.com /td SHA256 /a "$(APP)"
