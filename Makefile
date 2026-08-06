.PHONY: install patch banner docs

install:
	go install ./cmd/tpd

docs:
	go run ./cmd/gen-catalog

patch:
	git tag $$(mise exec -- svu patch) && git push origin --tags
