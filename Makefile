GO      ?= go
PREFIX  ?= /usr/local
LDFLAGS := -s -w
ARCHES  := amd64 arm64

all: $(ARCHES:%=dist/usbip-ssh_%)

payloads: $(ARCHES:%=embed/payload_%)

embed/payload_%: force
	@mkdir -p embed
	CGO_ENABLED=0 GOOS=linux GOARCH=$* $(GO) build -tags payload -ldflags '$(LDFLAGS)' -o $@ .

dist/usbip-ssh_%: payloads force
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=$* $(GO) build -ldflags '$(LDFLAGS)' -o $@ .

install: dist/usbip-ssh_$(shell $(GO) env GOHOSTARCH)
	install -D -m 755 $< $(DESTDIR)$(PREFIX)/bin/usbip-ssh

test: payloads
	$(GO) test ./...

clean:
	rm -rf dist embed

force:

.PHONY: all payloads install test clean force
