# Makefile for PRP GNS3 container
IMAGE ?= ghcr.io/grymme/prp-sim
TAG ?= latest

build:
	docker build -t $(IMAGE):$(TAG) .

run:
	docker run --rm --privileged --network=host \
		-v $(PWD)/tests/config.yaml:/etc/prp/config.yaml \
		$(IMAGE):$(TAG)

reload:
	@echo "Reload PRP config in a running container:"
	@echo "  docker exec <container> kill -HUP 1"
	@echo ""
	@echo "Or if you have the container name:"
	@printf '  docker exec prp-sim kill -HUP 1\n'

.PHONY: build run reload
