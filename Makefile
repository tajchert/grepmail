.PHONY: build install vet test fmt clean

BINARY := grepmail
LDFLAGS := -s -w

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/grepmail

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/grepmail

vet:
	go vet ./...

test:
	go test ./...

fmt:
	gofmt -s -w .

clean:
	rm -f $(BINARY)
