FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -o bot ./cmd/bot

FROM alpine:latest

WORKDIR /app

COPY --from=builder /app/bot .

EXPOSE 3000

ENTRYPOINT ["./bot"]
