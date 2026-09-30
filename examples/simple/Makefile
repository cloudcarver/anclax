SHELL := /bin/sh

.PHONY: dev reload db gen test ut

###################################################
### Dev 
###################################################

dev:
	docker compose up

reload:
	docker compose restart dev

db:
	psql "postgresql://postgres:postgres@localhost:5432/postgres?sslmode=disable"

gen:
	anclax gen

test:
	go test ./...

ut:
	@COLOR=ALWAYS go test -race -covermode=atomic -coverprofile=coverage.out -tags ut ./... 
	@go tool cover -html coverage.out -o coverage.html
	@go tool cover -func coverage.out | fgrep total | awk '{print "Coverage:", $$3}'
