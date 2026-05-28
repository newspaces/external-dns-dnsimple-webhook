IMAGE ?= ghcr.io/example/external-dns-dnsimple-webhook
TAG ?= latest

.PHONY: test
test:
	go test ./...

.PHONY: build
build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/external-dns-dnsimple-webhook .

.PHONY: docker-build
docker-build:
	docker build -t $(IMAGE):$(TAG) .
