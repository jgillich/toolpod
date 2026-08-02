.PHONY: install patch

install:
	go install ./cmd/tpod

patch:
	git tag $$(svu patch) && git push origin --tags
