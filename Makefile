include .env
include Makefile.local

DOCKER_IMAGE ?= bitbucket-slack-bot

.PHONY: build
build:
	mkdir -p build
	go build -o ./build/bot ./cmd/bot

.PHONY: start
start: build
	./build/bot \
	--slack-bot-token=${SLACK_BOT_TOKEN} \
	--slack-signing-secret=${SLACK_SIGNING_SECRET} \
	--bitbucket-client-id=${BITBUCKET_CLIENT_ID} \
	--bitbucket-client-secret=${BITBUCKET_CLIENT_SECRET} \
	--db-url=${DATABASE_URL} \
	--public-url=${PUBLIC_URL} \
--addr=:3000

.PHONY: docker-build docker-start
docker-build:
	docker buildx build -t $(DOCKER_IMAGE) .

docker-start: docker-build
	docker run --rm \
		--network=host \
		$(DOCKER_IMAGE) \
		--slack-bot-token=${SLACK_BOT_TOKEN} \
		--slack-signing-secret=${SLACK_SIGNING_SECRET} \
		--bitbucket-client-id=${BITBUCKET_CLIENT_ID} \
		--bitbucket-client-secret=${BITBUCKET_CLIENT_SECRET} \
		--db-url=${DATABASE_URL} \
		--public-url=${PUBLIC_URL} \
		--addr=:3000

.PHONY: format format-fix
format:
	gofmt -l $$(find . -name '*.go')

format-fix:
	go fmt ./...
