# Release: binarios para todas las plataformas, archivos comprimidos y checksums.
# Uso: make -f dist.mk dist VERSION=v0.1.0
# Sobrescribe lo que ya exista en dist/ (no borra nada).

GO       ?= go
VERSION  ?= v0.1.0
PKG      := ./cmd/factory
DIST     := dist
LDFLAGS  := -s -w -X main.version=$(VERSION)
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

.PHONY: dist

dist:
	mkdir -p $(DIST)/.stage
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		name=factory_$(VERSION)_$${os}_$${arch}; \
		ext=""; if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
		echo "build $$name"; \
		mkdir -p $(DIST)/.stage/$$name; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/.stage/$$name/factory$$ext $(PKG) || exit 1; \
		if [ "$$os" = "windows" ]; then \
			(cd $(DIST)/.stage && zip -qr ../$$name.zip $$name); \
		else \
			tar -C $(DIST)/.stage -czf $(DIST)/$$name.tar.gz $$name; \
		fi; \
	done
	cd $(DIST) && sha256sum factory_*.tar.gz factory_*.zip > checksums.txt
	@echo "listo: $(DIST)/"
