SHELL := /bin/sh

.PHONY: setup dev reload db gen test ut

###################################################
### Dev 
###################################################

dev: setup
	docker compose up

setup:
	sh dev/setup.sh

reload:
	docker compose restart dev

db:
	docker compose exec db psql -U postgres -d postgres

gen:
	anclax gen

test:
	go test ./...

ut:
	@COLOR=ALWAYS go test -race -covermode=atomic -coverprofile=coverage.out -tags ut ./... 
	@go tool cover -html coverage.out -o coverage.html
	@go tool cover -func coverage.out | fgrep total | awk '{print "Coverage:", $$3}'
