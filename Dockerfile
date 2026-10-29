# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM node:24-alpine AS console
WORKDIR /app/web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=console /app/web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build -trimpath -ldflags="-s -w" -o /gateway ./cmd/gateway

FROM alpine:3.23
LABEL org.opencontainers.image.source="https://github.com/guohai163/openai-gateway" \
      org.opencontainers.image.title="AI Gateway" \
      org.opencontainers.image.description="OpenAI-compatible gateway with private keys, IP allowlists and a bilingual admin console"
RUN apk add --no-cache ca-certificates tzdata && addgroup -S gateway && adduser -S -G gateway gateway
COPY --from=builder /gateway /usr/local/bin/gateway
USER gateway
EXPOSE 8080
ENTRYPOINT ["gateway"]
