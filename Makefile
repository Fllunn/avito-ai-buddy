.PHONY: test test-backend test-mocks test-frontend build coverage-backend-native coverage
.PHONY: up down infra-check

test: test-backend test-mocks test-frontend

test-backend:
	cd backend && go test -race ./...

test-mocks:
	cd mocks && go test -race ./...

test-frontend:
	cd frontend && npm test

build:
	cd backend && go build -o bin/server ./cmd/server
	cd mocks && go build -o bin/server ./cmd/server
	cd frontend && npm run build

# Нативная статистика операторов; общий отчёт строк — make coverage.
coverage-backend-native:
	cd backend && go test -coverpkg=./platform/... -coverprofile=coverage.out ./...
	cd backend && go tool cover -func=coverage.out

coverage:
	./scripts/coverage/run.sh

up:
	docker compose up -d --build --wait

down:
	docker compose down

infra-check:
	./scripts/check-compose.sh

.PHONY: integration-test
integration-test:
	./scripts/test-services.sh $(MAKE) test-backend test-mocks
