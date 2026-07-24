# Makefile for PRP GNS3 container
build:
	docker build -t ghcr.io/grymme/prp-sim:latest .

run:
	docker run --rm --privileged --network=host \
		-v $(PWD)/tests/config.yaml:/etc/prp/config.yaml \
		ghcr.io/grymme/prp-sim:latest