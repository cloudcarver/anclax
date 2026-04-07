SHELL := /bin/zsh
PROJECT_DIR=$(shell pwd)
ANCLAX_VERSION=$(shell cat VERSION)

# Test timeouts
UT_TIMEOUT ?= 6m
DST_TIMEOUT ?= 90s
SMOKE_TIMEOUT ?= 90s
SMOKE_STRESS_TIMEOUT ?= 220s
CHAOS_TIMEOUT ?= 60m

ANCLAX_TASKCORE_CHAOS_ITERATIONS ?= 200

.PHONY: dev chaos


###################################################
### Dev 
###################################################

dev:
	docker-compose up

reload:
	docker-compose restart dev

db:
	psql "postgresql://postgres:postgres@localhost:5432/postgres?sslmode=disable"

prepare-test:
	cd test && uv sync
	GOCACHE=/tmp/go-cache go run ./cmd/anclax bundle-openapi-spec --input api/openapi --output /tmp/anclax-openapi-bundle.yaml
	cd test && uv run openapi-python-client generate --path /tmp/anclax-openapi-bundle.yaml --output-path oapi --overwrite 

python-test: prepare-test
	cd test && uv run main.py

test: ut test-deterministic smoke

ut:
	@COLOR=ALWAYS go test -race -covermode=atomic -coverprofile=coverage.out -tags ut ./... -timeout $(UT_TIMEOUT)
	@go tool cover -html coverage.out -o coverage.html
	@go tool cover -func coverage.out | fgrep total | awk '{print "Coverage:", $$3}'

smoke:
	GOCACHE=/tmp/go-cache go run ./cmd/anclax gen
	GOCACHE=/tmp/go-cache go test -tags=smoke ./pkg/taskcore/e2e -run TestDSTTaskStoreScenariosSmoke -count=1 -v -timeout $(SMOKE_TIMEOUT)
	GOCACHE=/tmp/go-cache go test -tags=smoke ./pkg/taskcore/e2e -run TestDSTTaskStoreScenariosStressSmoke -count=1 -v -timeout $(SMOKE_STRESS_TIMEOUT)

smoke-worker: smoke

chaos:
	GOCACHE=/tmp/go-cache ANCLAX_TASKCORE_CHAOS_ITERATIONS=$(ANCLAX_TASKCORE_CHAOS_ITERATIONS) go test -tags=smoke ./pkg/taskcore/chaos -run TestContainerizedTaskcoreChaosSmoke -count=1 -v -timeout $(CHAOS_TIMEOUT)

test-deterministic:
	GOCACHE=/tmp/go-cache go run ./cmd/anclax gen
	GOCACHE=/tmp/go-cache go test ./pkg/taskcore/dtmtest -count=1 -v -timeout $(DST_TIMEOUT)

gen:
	sed -i -E '/^replace github\.com\/cloudcarver\/anclax => \.\.\/\.\.\/?$$/d' examples/simple/go.mod
	sed -i -E 's@(github.com/cloudcarver/anclax )v[^ ]+@\1$(ANCLAX_VERSION)@' examples/simple/go.mod
	go run cmd/dev/main.go copy-templates --src examples/simple --dst cmd/anclax/initFiles --exclude .anclax,go.sum
	sed -i -E '/^replace github\.com\/cloudcarver\/anclax => \.\.\/\.\.\/?$$/d' cmd/anclax/initFiles/go.mod.embed
	sed -i -E 's@(github.com/cloudcarver/anclax )v[^ ]+@\1$(ANCLAX_VERSION)@' cmd/anclax/initFiles/go.mod.embed

install: gen
	go install ./cmd/anclax
