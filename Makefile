# Makefile for PRP GNS3 container
build:
	docker build -t ghcr.io/westermo/prp-gns3:latest .

run:
	docker run --rm --privileged --network=host \
		-v $(PWD)/tests/config.yaml:/etc/prp/config.yaml \
		ghcr.io/westermo/prp-gns3:latest