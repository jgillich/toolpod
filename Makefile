.PHONY: install patch banner

install:
	go install ./cmd/tpd

patch:
	git tag $$(mise exec -- svu patch) && git push origin --tags

banner: assets/tpd-banner-light.svg

assets/tpd-banner-light.svg: assets/tpd-banner.svg scripts/generate-banner.py
	python3 scripts/generate-banner.py
