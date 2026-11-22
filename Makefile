BINARY  := bin/factory
PKG     := ./cmd/factory
PREFIX  ?= $(HOME)/.local
CONFIG  ?= /piscina/workspace/academia/factory.yaml
# Subcomando que ejecuta `make run`. Cambiar a `run` cuando exista el orquestador.
CMD     ?= check

GO      ?= go

.PHONY: build run install uninstall clean vet

build:
	@mkdir -p bin
	$(GO) build -o $(BINARY) $(PKG)

run: build
	./$(BINARY) $(CMD) -c $(CONFIG)

install: build
	install -d $(PREFIX)/bin
	install -m 755 $(BINARY) $(PREFIX)/bin/factory
	@echo "instalado en $(PREFIX)/bin/factory"

uninstall:
	rm -f $(PREFIX)/bin/factory

vet:
	$(GO) vet ./...

clean:
	rm -rf bin
