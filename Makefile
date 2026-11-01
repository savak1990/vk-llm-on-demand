.PHONY: help specs-check

.DEFAULT_GOAL := help

help: ## Print every target with its doc comment
	@awk 'BEGIN{FS=":.*## "} /^[a-zA-Z0-9_-]+:.*## /{printf "  %-16s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

specs-check: ## Check the specs/ layout, front matter and relative links
	@./scripts/specs-check.sh
