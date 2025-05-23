SHELL := /bin/zsh
PROJECT_DIR=$(shell pwd)


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

gen:
	go run cmd/dev/main.go copy-templates --src examples/simple --dst cmd/anchor/initFiles --exclude .anchor,go.sum
