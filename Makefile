SHELL := /bin/zsh
PROJECT_DIR=$(shell pwd)

gen-frontend-client:
	cd web && pnpm run gen

###################################################
### Common
###################################################

gen: gen-frontend-client
	go run ./cmd/anchor gen
	@go mod tidy


###################################################
### Dev enviornment
###################################################

dev:
	docker-compose up

reload:
	docker-compose restart dev

db:
	psql "postgresql://postgres:postgres@localhost:5432/postgres?sslmode=disable"

test:
	TEST_DIR=$(PROJECT_DIR)/e2e HOLD="$(HOLD)" ./scripts/run-local-test.sh "$(K)" 


###################################################
### Build
###################################################

VERSION=v0.2.0

build-web:
	@cd web && pnpm run build


build-server:
	GOOS=linux GOARCH=amd64 go build -o ./bin/anchor-server-amd64 cmd/server/main.go
	GOOS=linux GOARCH=arm64 go build -o ./bin/anchor-server-arm64 cmd/server/main.go

IMG_TAG=$(VERSION)
DOCKER_REPO=cloudcarver/anchor

push-docker: build-server
	docker buildx build --platform linux/amd64,linux/arm64 -f docker/Dockerfile.pgbundle -t ${DOCKER_REPO}:${IMG_TAG}-pgbundle --push .
	docker buildx build --platform linux/amd64,linux/arm64 -f docker/Dockerfile -t ${DOCKER_REPO}:${IMG_TAG} --push .

ci: doc build-web build-server build-docker build-binary docker-push binary-push

push: docker-push

ut:
	@COLOR=ALWAYS go test -race -covermode=atomic -coverprofile=coverage.out -tags ut ./... 
	@go tool cover -html coverage.out -o coverage.html
	@go tool cover -func coverage.out | fgrep total | awk '{print "Coverage:", $$3}'
