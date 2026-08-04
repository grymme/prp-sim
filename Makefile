# Makefile for PRP GNS3 container
IMAGE ?= ghcr.io/grymme/prp-sim
TAG ?= latest

build:
	docker build -t $(IMAGE):$(TAG) .

run:
	docker run --rm --privileged \
		--network prp-sim-bridge \
		-v $(PWD)/tests/configs/redbox-a.yaml:/etc/prp/config.yaml \
		$(IMAGE):$(TAG)

reload:
	@echo "Reload PRP config in a running container:"
	@echo "  docker exec <container> kill -HUP 1"
	@echo ""
	@echo "Or if you have the container name:"
	@printf '  docker exec prp-sim kill -HUP 1\n'

.PHONY: build run reload

# Create a dedicated bridge network for the simulator (see README for why
# --network=host is discouraged).
network:
	docker network create prp-sim-bridge 2>/dev/null || true
	@echo "created/verified docker network prp-sim-bridge"

.PHONY: build run reload network
