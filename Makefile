BINARY := givi

VERSION ?= $(shell go run . 2>/dev/null)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build install test clean release-preview

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) .

install:
	CGO_ENABLED=0 go install -trimpath -ldflags '$(LDFLAGS)' .

vet:
	go vet ./...

test:
	go test -v -count=1 ./...

clean:
	rm -rf $(BINARY) dist/

## release-preview: build the release binary for the current platform/arch only,
## via GoReleaser, without publishing anything (no git tag, no GitHub release, no
## image push). Output lands in dist/. Uses givi's own computed version.
release-preview:
	GORELEASER_CURRENT_TAG=$(VERSION) goreleaser build --clean --snapshot --single-target
