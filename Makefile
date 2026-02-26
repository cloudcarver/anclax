SHELL := /bin/zsh
PROJECT_DIR=$(shell pwd)
ANCLAX_VERSION=$(shell cat VERSION)


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
	cd test && uv run openapi-python-client generate --path ../api/v1.yaml --output-path oapi --overwrite 

test: prepare-test
	cd test && uv run main.py

ut:
	@COLOR=ALWAYS go test -race -covermode=atomic -coverprofile=coverage.out -tags ut ./... 
	@go tool cover -html coverage.out -o coverage.html
	@go tool cover -func coverage.out | fgrep total | awk '{print "Coverage:", $$3}'

smoke-worker:
	GOCACHE=/tmp/go-cache go run ./cmd/anclax gen
	GOCACHE=/tmp/go-cache go test -tags=smoke ./pkg/taskcore/e2e -run TestDSTTaskStoreScenariosSmoke -count=1 -v
	GOCACHE=/tmp/go-cache go test -tags=smoke ./pkg/taskcore/e2e -run TestDSTTaskStoreScenariosStressSmoke -count=1 -v

smoke-worker-v2:
	GOCACHE=/tmp/go-cache go run ./cmd/anclax gen
	GOCACHE=/tmp/go-cache go test -tags=smoke ./pkg/taskcore/e2ev2 -run TestDSTTaskStoreScenariosSmoke -count=1 -v
	GOCACHE=/tmp/go-cache go test -tags=smoke ./pkg/taskcore/e2ev2 -run TestDSTTaskStoreScenariosStressSmoke -count=1 -v

smoke-worker-bench:
	GOCACHE=/tmp/go-cache go run ./cmd/anclax gen
	GOCACHE=/tmp/go-cache go test -tags=smoke ./pkg/taskcore/e2e -run TestWorkerStressE2E_SingleWorkerConcurrencyRegression -count=1 -v
	GOCACHE=/tmp/go-cache go test -tags=smoke ./pkg/taskcore/e2e -run TestWorkerStressE2E_MultiWorkerLabelsWeightsBenchmark -count=1 -v

dtmtest:
	GOCACHE=/tmp/go-cache go run ./cmd/anclax gen
	GOCACHE=/tmp/go-cache go test ./pkg/taskcore/dtmtest -count=1 -v

gen:
	go run cmd/dev/main.go copy-templates --src examples/simple --dst cmd/anclax/initFiles --exclude .anclax,go.sum
	sed -i -E 's@(github.com/cloudcarver/anclax )v[^ ]+@\1$(ANCLAX_VERSION)@' cmd/anclax/initFiles/go.mod.embed

install: gen
	go install ./cmd/anclax
