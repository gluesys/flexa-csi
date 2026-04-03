#  Copyright 2025 Gluesys FlexA Inc.

REGISTRY_NAME?=ghcr.io/gluesys
IMAGE_NAME?=flexa-csi
IMAGE_VERSION?=1.0.0
IMAGE_TAG=$(REGISTRY_NAME)/$(IMAGE_NAME):$(IMAGE_VERSION)
IMAGE_TAG_LATEST=$(REGISTRY_NAME)/$(IMAGE_NAME):latest

# For now, only build linux/amd64 platform
ifeq ($(GOARCH),)
GOARCH:=amd64
endif
GOARM?=""
BUILD_ENV=CGO_ENABLED=0 GOOS=linux GOARCH=$(GOARCH) GOARM=$(GOARM)
BUILD_FLAGS="-s -w -extldflags \"-static\""

.PHONY: all clean flexa-csi-driver docker-build docker-tag-latest docker-push docker-push-latest docker-publish

all: flexa-csi-driver

flexa-csi-driver:
	@mkdir -p bin
	$(BUILD_ENV) go build -v -ldflags $(BUILD_FLAGS) -o ./bin/flexa-csi-driver ./

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
	-rm -rf ./bin

