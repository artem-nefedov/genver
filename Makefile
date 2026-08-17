BINARY := genver

VERSION ?= $(shell go run . --format='v{{.Core}}-{{substr 0 7 .HeadHash}}' 2>/dev/null)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build install fix test clean release-preview

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) .

install:
	CGO_ENABLED=0 go install -trimpath -ldflags '$(LDFLAGS)' .

fix:
	go fmt ./...
	go fix ./...
	go vet -fix ./...
	go mod tidy

test:
	go test -v -count=1 ./...

clean:
	rm -rf $(BINARY) dist/

## release-preview: build the release binary for the current platform/arch only,
## via GoReleaser, without publishing anything (no git tag, no GitHub release, no
## image push). Output lands in dist/. Uses genver's own computed version.
release-preview:
	GORELEASER_CURRENT_TAG=$(VERSION) goreleaser build --clean --snapshot --single-target
