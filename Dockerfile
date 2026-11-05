FROM golang:1.27.3-alpine AS build

WORKDIR /src

COPY go.mod ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/import-legacy ./cmd/import-legacy

FROM alpine:3.22

RUN apk add --no-cache ca-certificates \
    && addgroup -S hikari \
    && adduser -S -G hikari -h /app hikari

WORKDIR /app

COPY --from=build /out/api /app/api
COPY --from=build /out/migrate /app/migrate
COPY --from=build /out/import-legacy /app/import-legacy
COPY migrations /app/migrations

USER hikari

EXPOSE 8080

ENTRYPOINT ["/app/api"]
