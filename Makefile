.PHONY: install patch banner docs

install:
	go install ./cmd/tpd

docs:
	go run ./cmd/gen-catalog

patch:
	version=$$(mise exec -- svu patch) && git tag "$$version" && git push origin --tags
