GO ?= go
GOLANGCI_LINT ?= golangci-lint
COVERAGE_MIN ?= 35.0

.PHONY: fmt fmt-check vet lint test race cover cover-check check check-full build

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
	$(GO) build -o 海涛中控v0.1.exe .
