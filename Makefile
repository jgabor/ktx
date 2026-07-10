.PHONY: check fmt fmt-check test

fmt:
	gofmt -w cmd internal tools/bench/data.go

fmt-check:
	test -z "$$(gofmt -l cmd internal tools/bench/data.go)"

test:
	go test ./...
	go vet ./...

check: fmt-check test
