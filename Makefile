VERSION ?= dev
IMAGE ?= ghcr.io/soliddowant/obot-authentik-provider:$(VERSION)

.PHONY: build test lint image e2e

# Builds the registry layout under build/registry, for running obot from source against it.
build:
	mkdir -p build/registry
	cp -r registry/. build/registry/
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o build/registry/authentik-auth-provider/bin/obot-provider .

test:
	go test -race -cover ./...

lint:
	golangci-lint run ./...

image:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE) .

# Runs obot with this registry mounted as an image volume, against a throwaway authentik.
e2e:
	./e2e/run.sh
