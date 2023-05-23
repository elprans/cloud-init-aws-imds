.PHONY: bin/cloud-init-aws-imds

PKG=github.com/elprans/cloud-init-aws-imds
VERSION=v0.0.8
GIT_COMMIT?=$(shell git rev-parse HEAD)
BUILD_DATE?=$(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS?="-X ${PKG}/pkg/driver.driverVersion=${VERSION} -X ${PKG}/pkg/driver.gitCommit=${GIT_COMMIT} -X ${PKG}/pkg/driver.buildDate=${BUILD_DATE} -s -w"

bin/cloud-init-aws-imds: | bin
	CGO_ENABLED=0 GOOS=linux go build -ldflags ${LDFLAGS} -o bin/cloud-init-aws-imds ./cmd/

bin:
	@mkdir -p $@
