GO_VERSION := 1.21

TAG := $(shell git describe --abbrev=0 --tags --always)
HASH := $(shell git rev-parse HEAD)
DATE := $(shell date +%Y-%m-%d.%H:%M:%S)
PWD := $(shell pwd)
LIST := $(shell ls -lah)

LDFLAGS := -w -X github.com/randsw/schema-registry-operator/handlers.hash=$(HASH) \
			-X github.com/randsw/schema-registry-operator/handlers.tag=$(TAG) \
			-X github.com/randsw/schema-registry-operator/handlers.date=$(DATE)

.PHONY: setup_git
setup_git:
	git config --global --add safe.directory $(realpath .)

.PHONY: build
build: setup_git
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -a -o sr-operator main.go

test:
	echo ${LIST}

pwd:
	echo ${PWD}