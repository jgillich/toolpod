.PHONY: install image

install:
	go install ./cmd/tpod

image:
	podman build -t ghcr.io/jgillich/tpod-mise .
