VERSION ?= $(shell grep -oP 'var Version = "\K[^"]+' main.go)
LDFLAGS = -s -w -X main.Version=$(VERSION)
BINARY = ho

# Parse semver for versioninfo.json patching
SEMVER = $(subst v,,$(VERSION))
VER_MAJOR = $(word 1,$(subst ., ,$(SEMVER)))
VER_MINOR = $(word 2,$(subst ., ,$(SEMVER)))
VER_PATCH = $(firstword $(subst -, ,$(word 3,$(subst ., ,$(SEMVER)))))

.PHONY: patch-version restore-version

patch-version:
	@sed -i 's/"Major": [0-9]*/"Major": $(or $(VER_MAJOR),0)/g' versioninfo.json
	@sed -i 's/"Minor": [0-9]*/"Minor": $(or $(VER_MINOR),0)/g' versioninfo.json
	@sed -i 's/"Patch": [0-9]*/"Patch": $(or $(VER_PATCH),0)/g' versioninfo.json
	@sed -i 's/"FileVersion": "[^"]*"/"FileVersion": "$(VERSION)"/' versioninfo.json
	@sed -i 's/"ProductVersion": "[^"]*"/"ProductVersion": "$(VERSION)"/' versioninfo.json

restore-version:
	@git checkout versioninfo.json 2>/dev/null || true

.PHONY: build build-linux build-darwin build-windows build-all clean run dev

build: patch-version
	go generate
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY).exe .
	@$(MAKE) restore-version

build-linux:
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-linux-amd64 .
	GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-linux-arm64 .

build-darwin:
	GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-darwin-amd64 .
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-darwin-arm64 .

build-windows: patch-version
	go generate
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-windows-amd64.exe .
	@$(MAKE) restore-version

build-all: build-linux build-darwin build-windows

run: build
	./bin/$(BINARY).exe

dev:
	go run -tags dev .

clean:
	rm -rf bin/ data/
