.PHONY: build check-fmt ci fmt race staticcheck test verify vet

GO_FILES := $(shell find . -type f -name '*.go')
STATICCHECK_VERSION ?= v0.7.0

build:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/s3-copy-gateway .

check-fmt:
	@files="$$(gofmt -l $(GO_FILES))"; \
	if [ -n "$$files" ]; then \
		echo "The following Go files need formatting:"; \
		echo "$$files"; \
		exit 1; \
	fi

fmt:
	gofmt -w $(GO_FILES)

race:
	go test -race -shuffle=on -count=1 ./...

staticcheck:
	go run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) ./...

test:
	go test -shuffle=on -count=1 ./...

verify:
	go mod verify

vet:
	go vet ./...

ci: check-fmt verify vet staticcheck test race build
