GOBIN := ~/go/bin

.PHONY: setup build test lint run run-dev clean

setup:
	go install github.com/caddyserver/xcaddy/cmd/xcaddy@latest

build:
	cd $(shell mktemp -d) && $(GOBIN)/xcaddy build --output $(CURDIR)/caddy --with github.com/luzilla/caddyscope=$(CURDIR)

test:
	go test ./...

lint:
	test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)
	go vet ./...
	go tool revive -config revive.toml \
		-formatter stylish \
		-set_exit_status ./...

run: build
	./caddy run --config Caddyfile

run-dev: run

clean:
	rm -f caddy
