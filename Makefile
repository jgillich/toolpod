.PHONY: install image

install:
	go install ./cmd/toolpod

image:
	podman build -t ghcr.io/jgillich/toolpod-mise .
