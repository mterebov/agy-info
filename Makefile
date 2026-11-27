BIN := bin/agy-info
DIST := dist
LDFLAGS := -s -w

.PHONY: build test vet ui install clean release-bins

build:
	@mkdir -p bin
	cd server && go build -trimpath -ldflags="$(LDFLAGS)" -o ../$(BIN) .

test:
	cd server && go test -v -race ./...

vet:
	cd server && go vet ./...

ui:
	cd ui && npm ci && npm run build

install:
	./scripts/install.sh

clean:
	rm -rf bin $(DIST)

release-bins:
	@mkdir -p $(DIST)
	cd server && CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o ../$(DIST)/agy-info-darwin-arm64 .
	cd server && CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o ../$(DIST)/agy-info-darwin-amd64 .
	cd server && CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o ../$(DIST)/agy-info-linux-amd64 .
	cd server && CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o ../$(DIST)/agy-info-linux-arm64 .
	cd server && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o ../$(DIST)/agy-info-windows-amd64.exe .
