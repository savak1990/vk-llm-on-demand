.PHONY: help specs-check

.DEFAULT_GOAL := help

help: ## Print every target with its doc comment
	@awk 'BEGIN{FS=":.*## "} /^[a-zA-Z0-9_-]+:.*## /{printf "  %-16s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

specs-check: ## Check the specs/ layout, front matter and relative links
	@./scripts/specs-check.sh

SVC ?=
CHART ?= gpu-on-demand
VERSION ?= $(shell ./scripts/version.sh base)
IMAGE_TAG ?= $(shell git rev-parse HEAD)
IMAGE ?= ghcr.io/savak1990/vk-llm-on-demand/$(SVC):$(IMAGE_TAG)

.PHONY: go-build go-test go-lint lint-worker-script test-worker-script test-worker-image require-svc image-build image-push helm-lint helm-template helm-package helm-push version release buildx-init keygen repo-settings

go-build: ## Build every cmd/ binary into bin/
	go build -o bin/ ./cmd/...

go-test: ## Run all unit tests
	go test ./... -count=1

go-lint: ## Run golangci-lint
	golangci-lint run ./...

lint-worker-script: ## shellcheck the rendered worker onstart script
	shellcheck -s bash internal/worker/testdata/onstart.golden.sh

test-worker-script: ## Run the worker's stub-container Docker tests (AC6/AC7)
	go test -tags integration ./internal/worker/... -run TestWorkerScript -v

test-worker-image: ## Run the worker's real-image Docker test (AC8/AC9); needs Docker and network, not run in CI
	go test -tags integration_image ./internal/worker/... -run TestWorkerImage -v

require-svc: ## Fail unless SVC names an existing deploy/docker/<SVC>.Dockerfile
	@test -n "$(SVC)" || (echo "SVC is required, e.g. make image-build SVC=gpu-on-demand" && exit 1)
	@test -f deploy/docker/$(SVC).Dockerfile || (echo "no deploy/docker/$(SVC).Dockerfile" && exit 1)

image-build: require-svc ## Build a single-arch local image for SVC (docker buildx --load)
	docker buildx build --load -f deploy/docker/$(SVC).Dockerfile -t $(IMAGE) .

image-push: require-svc buildx-init ## Build and push a multi-arch image for SVC
	docker buildx build --builder vk --push --platform linux/amd64,linux/arm64 --provenance=false -f deploy/docker/$(SVC).Dockerfile -t $(IMAGE) .

HELM_TEST_SET = --set image.tag=test \
	--set existingSecrets.provider=p --set existingSecrets.apiKeys=k --set existingSecrets.ssh=s \
	--set config.model.url=https://example.com/m.gguf \
	--set config.model.sha256=e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855 \
	--set config.worker.image=example/worker \
	--set prometheusRule.enabled=true --set dashboard.enabled=true

helm-lint: ## Lint the deploy/helm/<CHART> chart
	helm lint deploy/helm/$(CHART) $(HELM_TEST_SET)

helm-template: ## Render the deploy/helm/<CHART> chart
	helm template deploy/helm/$(CHART) $(HELM_TEST_SET)

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
