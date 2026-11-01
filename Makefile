.PHONY: help specs-check

.DEFAULT_GOAL := help

help: ## Print every target with its doc comment
	@awk 'BEGIN{FS=":.*## "} /^[a-zA-Z0-9_-]+:.*## /{printf "  %-16s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

specs-check: ## Check the specs/ layout, front matter and relative links
	@./scripts/specs-check.sh

SVC ?=
CHART ?= gpu-on-demand
VERSION ?= $(shell ./scripts/version.sh base)
IMAGE ?= ghcr.io/savak1990/vk-llm-on-demand/$(SVC):$(VERSION)

.PHONY: go-build go-test go-lint require-svc image-build image-push helm-lint helm-template helm-package helm-push version release buildx-init keygen repo-settings

go-build: ## Build every cmd/ binary into bin/
	go build -o bin/ ./cmd/...

go-test: ## Run all unit tests
	go test ./... -count=1

go-lint: ## Run golangci-lint
	golangci-lint run ./...

require-svc: ## Fail unless SVC names an existing deploy/docker/<SVC>.Dockerfile
	@test -n "$(SVC)" || (echo "SVC is required, e.g. make image-build SVC=gpu-on-demand" && exit 1)
	@test -f deploy/docker/$(SVC).Dockerfile || (echo "no deploy/docker/$(SVC).Dockerfile" && exit 1)

image-build: require-svc ## Build a single-arch local image for SVC (docker buildx --load)
	docker buildx build --load -f deploy/docker/$(SVC).Dockerfile -t $(IMAGE) .

image-push: require-svc buildx-init ## Build and push a multi-arch image for SVC
	docker buildx build --push --platform linux/amd64,linux/arm64 -f deploy/docker/$(SVC).Dockerfile -t $(IMAGE) .

helm-lint: ## Lint the deploy/helm/<CHART> chart
	helm lint deploy/helm/$(CHART)

helm-template: ## Render the deploy/helm/<CHART> chart
	helm template deploy/helm/$(CHART)

helm-package: ## Package the chart at VERSION into dist/
	helm package deploy/helm/$(CHART) --version $(VERSION) --app-version $(VERSION) -d dist/

helm-push: ## Push the packaged chart OCI artifact
	helm push dist/$(CHART)-$(VERSION).tgz oci://ghcr.io/savak1990/vk-llm-on-demand/charts

version: ## Print the next version this commit would publish as
	./scripts/version.sh base

release: ## Trigger the release workflow with BUMP=patch|minor|major
	gh workflow run release.yml --ref main -f bump=$(BUMP)

buildx-init: ## Create the shared buildx builder if it doesn't exist
	docker buildx inspect vk >/dev/null 2>&1 || docker buildx create --name vk --use

keygen: ## Generate a new sk-vkllm- prefixed secret
	@echo "sk-vkllm-$$(openssl rand -base64 32 | tr '+/' '-_' | tr -d '=')"

repo-settings: ## Apply required branch protection and repo settings
	./scripts/repo-settings.sh
