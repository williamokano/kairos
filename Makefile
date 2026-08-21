.PHONY: build test race arch lint tidy cross clean smoke-llm

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

# smoke-llm invokes a REAL claude CLI for real (real tokens, real
# wall-clock time) end to end through a real kairos daemon — the one
# deliberate, opt-in exception to "no test here calls a real LLM CLI".
# Never run by `make test`/`make race`/CI. See
# L22-harness-integration.md's "Real end-to-end smoke test" section.
smoke-llm:
	KAIROS_REAL_LLM_SMOKE=1 go test ./cmd/kairos/ -run TestRealLLMSmoke_Claude -v
