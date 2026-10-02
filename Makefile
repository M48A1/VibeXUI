.PHONY: build test linux
build:
	mkdir -p dist
	go build -trimpath -o dist/vibexui-panel ./cmd/panel
	go build -trimpath -o dist/vibexui-agent ./cmd/agent
test:
	go test -race ./...
	bash -n scripts/install.sh internal/bootstrap/agent-install.sh
	python3 scripts/test_install.py
linux:
	mkdir -p dist/linux-amd64 dist/linux-arm64
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o dist/linux-amd64/vibexui-panel ./cmd/panel
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o dist/linux-amd64/vibexui-agent ./cmd/agent
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o dist/linux-arm64/vibexui-panel ./cmd/panel
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o dist/linux-arm64/vibexui-agent ./cmd/agent
