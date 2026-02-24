include .env
include Makefile.local

DOCKER_IMAGE ?= git-slack-bot

.PHONY: build
build:
	mkdir -p build
	go build -o ./build/bot ./cmd/bot

.PHONY: start
start:
	./build/bot \
	--git-provider=bitbucket \
  	--slack-bot-token=${SLACK_BOT_TOKEN} \
  	--slack-signing-secret=${SLACK_SIGNING_SECRET} \
  	--api-key=${API_KEY} \
  	--db-url=${DATABASE_URL} \
  	--addr=:3000

.PHONY: docker-build docker-start
docker-build:
	docker build -t $(DOCKER_IMAGE) .

docker-start: docker-build
	docker run --rm \
		--network=host \
		$(DOCKER_IMAGE) \
		--git-provider=bitbucket \
		--slack-bot-token=${SLACK_BOT_TOKEN} \
		--slack-signing-secret=${SLACK_SIGNING_SECRET} \
		--api-key=${API_KEY} \
		--db-url=${DATABASE_URL} \
		--addr=:3000

.PHONY: format format-fix
format:
	gofmt -l $$(find . -name '*.go')

format-fix:
	go fmt ./...
