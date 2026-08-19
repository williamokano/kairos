.PHONY: build test race arch lint tidy cross clean

build:
	go build ./...

test:
	go test ./...

race:
	go test ./... -race

arch:
	go test ./internal/archtest/...

lint:
	golangci-lint run

tidy:
	go mod tidy

cross:
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -o /dev/null ./cmd/kairos
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o /dev/null ./cmd/kairos
	CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go build -o /dev/null ./cmd/kairos
	CGO_ENABLED=0 GOOS=linux  GOARCH=arm64 go build -o /dev/null ./cmd/kairos

clean:
	go clean ./...
