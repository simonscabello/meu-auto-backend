# Meu Auto — backend
#
# Requer um shell POSIX. No Windows, use o Git Bash.
# Sem `make` instalado, os comandos equivalentes estao em cada alvo abaixo.

GO           ?= go
BIN          := bin/meu-auto-api
# Pinned: sqlc infers nullability, and an unannounced version bump can change a generated
# struct without a single line of SQL changing (SPEC.md D-09).
SQLC_VERSION ?= v1.31.1

.DEFAULT_GOAL := help

## help: lista os alvos disponiveis
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'

## db-up: sobe o Postgres local
db-up:
	docker compose up -d
	@echo "aguardando o Postgres ficar saudavel..."
	@until [ "$$(docker inspect -f '{{.State.Health.Status}}' meuauto-postgres 2>/dev/null)" = "healthy" ]; do sleep 1; done
	@echo "Postgres pronto em localhost:5432"

## db-down: para o Postgres (mantem os dados)
db-down:
	docker compose down

## db-reset: APAGA o banco local e sobe do zero
db-reset:
	docker compose down -v
	$(MAKE) db-up

## run: roda a API local, carregando o .env
run:
	@test -f .env || (echo "faltando .env — copie de .env.example" && exit 1)
	@set -a && . ./.env && set +a && $(GO) run ./cmd/api

## build: compila o binario em bin/
build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="-s -w" -o $(BIN) ./cmd/api

## test: roda a suite inteira (a de integracao sobe um Postgres via testcontainers)
test:
	$(GO) test ./...

## test-unit: so os testes que nao tocam banco — o loop rapido
test-unit:
	$(GO) test ./internal/...

## test-integration: so a suite de integracao (exige Docker rodando)
test-integration:
	$(GO) test ./test/... -v

## test-golden: regera os snapshots em test/golden — LEIA o diff antes de commitar
test-golden:
	$(GO) test ./test/integration -run TestGoldenResponses -update

## test-race: roda os testes com o detector de corrida (exige cgo/gcc — usado no CI)
test-race:
	CGO_ENABLED=1 $(GO) test -race ./...

## test-cover: roda os testes e abre o relatorio de cobertura
test-cover:
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out

## vet: analise estatica da stdlib
vet:
	$(GO) vet ./...

## tidy: sincroniza go.mod e go.sum com os imports
tidy:
	$(GO) mod tidy

## sqlc: gera o codigo de acesso a dados a partir de db/queries
sqlc:
	$(GO) run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION) generate

## migrate-create: cria um par de migrations — make migrate-create name=create_users
migrate-create:
	@test -n "$(name)" || (echo "uso: make migrate-create name=create_users" && exit 1)
	@next=$$(printf "%06d" $$(( $$(ls db/migrations/*.up.sql 2>/dev/null | wc -l) + 1 ))); \
	  touch "db/migrations/$${next}_$(name).up.sql" "db/migrations/$${next}_$(name).down.sql"; \
	  echo "criado db/migrations/$${next}_$(name).{up,down}.sql"

.PHONY: help db-up db-down db-reset run build test test-unit test-integration test-golden test-race test-cover vet tidy sqlc migrate-create
