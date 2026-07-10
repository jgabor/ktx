.PHONY: check fmt fmt-check go-test python-test lint shell-check

fmt:
	gofmt -w cmd internal tools/bench/data.go
	ruff format scripts tools/bench

fmt-check:
	test -z "$$(gofmt -l cmd internal tools/bench/data.go)"
	ruff format --check scripts tools/bench

go-test:
	go test ./...
	go vet ./...

python-test:
	PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s scripts -p 'test_*.py'
	PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s tools/bench/harness/tests -p 'test_*.py'

lint:
	ruff check scripts tools/bench

shell-check:
	for file in scripts/*.sh tools/bench/ktx-runner; do bash -n "$$file"; done

check: fmt-check go-test python-test lint shell-check
