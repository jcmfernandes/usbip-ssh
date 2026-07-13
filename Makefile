SHELL := bash
.ONESHELL:
.SHELLFLAGS := -eu -o pipefail -c
.DELETE_ON_ERROR:
MAKEFLAGS += --warn-undefined-variables
MAKEFLAGS += --no-builtin-rules

ifeq ($(origin .RECIPEPREFIX), undefined)
  $(error This Make does not support .RECIPEPREFIX. Please use GNU Make 4.0 or later)
endif
.RECIPEPREFIX = >

GO      ?= go
# not named UPX: upx reads an environment variable of that name as its
# default options, and 'make UPX=...' would export it into the recipe
UPX_BIN ?= upx
PREFIX  ?= /usr/local
DESTDIR ?=
LDFLAGS := -s -w
ARCHES  := amd64 arm64

all: $(ARCHES:%=dist/usbip-ssh_%)

payloads: $(ARCHES:%=embed/payload_%)

# The payload/fat targets depend on the phony 'force' on purpose: make
# cannot track Go source dependencies, so up-to-date checks are delegated
# to Go's own build cache.
embed/payload_%: force
> mkdir -p embed
> CGO_ENABLED=0 GOOS=linux GOARCH=$* $(GO) build -tags payload -ldflags '$(LDFLAGS)' -o $@ .

dist/usbip-ssh_%: payloads force
> mkdir -p dist
> CGO_ENABLED=0 GOOS=linux GOARCH=$* $(GO) build -ldflags '$(LDFLAGS)' -o $@ .
> $(UPX_BIN) -q --best --lzma $@

install: dist/usbip-ssh_$(shell $(GO) env GOHOSTARCH)
> install -D -m 755 $< $(DESTDIR)$(PREFIX)/bin/usbip-ssh

test: payloads
> CGO_ENABLED=0 $(GO) test ./...

e2e: all
> CGO_ENABLED=0 $(GO) test -tags e2e -count=1 -timeout 10m -v ./e2e/

clean:
> rm -rf dist embed

force:

.PHONY: all payloads install test e2e clean force
