UNAME_S := $(shell uname -s)

ifeq ($(OS),Windows_NT)
PLUGIN_EXT := dll
else ifeq ($(UNAME_S),Darwin)
PLUGIN_EXT := dylib
else
PLUGIN_EXT := so
endif

PLUGIN_NAME := respfilter
PLUGIN_BIN := $(PLUGIN_NAME).$(PLUGIN_EXT)

.PHONY: build clean test

build:
	CGO_ENABLED=1 go build -buildmode=c-shared -o $(PLUGIN_BIN) .
	@rm -f $(PLUGIN_NAME).h

clean:
	rm -f $(PLUGIN_BIN) $(PLUGIN_NAME).h

test:
	go vet ./...
