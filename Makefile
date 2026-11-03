.PHONY: run build test cover fmt vet check clean

ADDR ?= :6379

## run: start the server (override the port with: make run ADDR=:6380)
run:
	go run . -addr $(ADDR)

## build: compile to bin/redis-go
build:
	go build -o bin/redis-go .

## test: run all tests with the race detector
test:
	go test -race ./...

## cover: show test coverage per package
cover:
	go test -cover ./...

## fmt: format all Go code
fmt:
	gofmt -w .

## vet: run Go's built-in static checks
vet:
	go vet ./...

## check: everything CI runs (formatting, vet, tests)
check:
	@test -z "$$(gofmt -l .)" || (echo "Run 'make fmt' to format:"; gofmt -l .; exit 1)
	go vet ./...
	go test -race ./...

clean:
	rm -rf bin
