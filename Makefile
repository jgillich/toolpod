.PHONY: install patch

install:
	go install ./cmd/tpd

patch:
	git tag $$(mise exec -- svu patch) && git push origin --tags
