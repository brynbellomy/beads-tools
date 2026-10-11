.PHONY: build test vet mocks

MOCKERY = go tool -modfile=tools/go.mod mockery

build:
	go build -o bin/beads-tools ./cmd/beads-tools

test:
	go test ./...

vet:
	go vet ./...

mocks:
	$(MOCKERY)
