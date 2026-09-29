PREFIX ?= $(HOME)/.local

.PHONY: build install test clean

build:
	go build -o bin/decision-tree .

install: build
	mkdir -p $(PREFIX)/bin
	install -m 755 bin/decision-tree $(PREFIX)/bin/decision-tree

test:
	go vet ./...
	go test ./...

clean:
	rm -rf bin
