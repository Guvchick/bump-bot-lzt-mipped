# ---- build stage ----
FROM golang:1.26-alpine AS build
WORKDIR /src

# Cache deps first.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# CGO is not needed (modernc.org/sqlite is pure Go).
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bot ./cmd/bot

# ---- runtime stage ----
FROM alpine:3.20
# su-exec lets the entrypoint drop from root to 'bot' after fixing /data perms.
RUN apk add --no-cache ca-certificates tzdata su-exec && adduser -D -u 10001 bot
WORKDIR /app

COPY --from=build /out/bot /app/bot
COPY docker-entrypoint.sh /app/docker-entrypoint.sh
RUN chmod +x /app/docker-entrypoint.sh

# Persist the SQLite DB on a volume; point DB_PATH at it.
ENV DB_PATH=/data/bot.db
VOLUME ["/data"]

# Starts as root so the entrypoint can chown /data, then execs the bot as 'bot'.
ENTRYPOINT ["/app/docker-entrypoint.sh"]
