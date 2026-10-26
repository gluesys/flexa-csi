#  Copyright 2025 Gluesys FlexA Inc.

REGISTRY_NAME?=ghcr.io/gluesys
IMAGE_NAME?=flexa-csi
IMAGE_VERSION?=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
IMAGE_TAG=$(REGISTRY_NAME)/$(IMAGE_NAME):$(IMAGE_VERSION)
IMAGE_TAG_LATEST=$(REGISTRY_NAME)/$(IMAGE_NAME):latest

CHART_REGISTRY?=oci://ghcr.io/gluesys
CHART_VERSION_SUFFIX?=

# For now, only build linux/amd64 platform
ifeq ($(GOARCH),)
GOARCH:=amd64
endif
GOARM?=""
BUILD_ENV=CGO_ENABLED=0 GOOS=linux GOARCH=$(GOARCH) GOARM=$(GOARM)
BUILD_FLAGS="-s -w -extldflags \"-static\""

.PHONY: all clean flexa-csi-driver docker-build docker-tag-latest docker-push docker-push-latest docker-publish
.PHONY: test verify fmt fmt-check vet lint chart-package chart-push

all: flexa-csi-driver

flexa-csi-driver:
	@mkdir -p bin
	$(BUILD_ENV) go build -v -ldflags $(BUILD_FLAGS) -o ./bin/flexa-csi-driver ./

# --- CI gate ---

test:
	go test -race -count=1 ./...

verify: flexa-csi-driver test ## Blocking CI gate: what is already green on this tree

fmt:
	gofmt -w $(shell git ls-files '*.go')

fmt-check:
	@files=$$(gofmt -l $$(git ls-files '*.go')); \
	if [ -n "$$files" ]; then echo "gofmt needed:"; echo "$$files"; exit 1; fi

vet:
	go vet ./...

lint: ## Run golangci-lint (installed via apk or from upstream releases)
	golangci-lint run --timeout=5m ./...

# --- Helm chart (OCI) ---

chart-package: ## Package the chart into dist/ (CHART_VERSION_SUFFIX e.g. -ci.<sha>)
	@mkdir -p dist
	helm package charts/flexa-csi \
	  --version "$$(awk '/^version:/{print $$2}' charts/flexa-csi/Chart.yaml)$(CHART_VERSION_SUFFIX)" \
	  --app-version "$$(awk '/^appVersion:/{print $$2}' charts/flexa-csi/Chart.yaml)" \
	  --destination dist

chart-push: chart-package ## Push the packaged chart to CHART_REGISTRY (helm registry login first)
	helm push dist/flexa-csi-*.tgz $(CHART_REGISTRY)

docker-build:
	docker build -f Dockerfile -t $(IMAGE_TAG) .

docker-tag-latest:
	docker tag $(IMAGE_TAG) $(IMAGE_TAG_LATEST)

docker-push:
	docker push $(IMAGE_TAG)

docker-push-latest:
	docker push $(IMAGE_TAG_LATEST)

docker-publish: docker-build docker-tag-latest docker-push docker-push-latest

clean:
	-rm -rf ./bin ./dist

