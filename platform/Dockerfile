# --- build stage ---
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/ntm-platform .

# --- runtime stage ---
FROM alpine:3.20
RUN apk add --no-cache ca-certificates openssl curl && adduser -D -u 10001 ntm
WORKDIR /app
COPY --from=build /out/ntm-platform /app/ntm-platform
COPY entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh && mkdir -p /certs && chown ntm:ntm /certs
EXPOSE 8443
USER ntm
ENTRYPOINT ["/app/entrypoint.sh"]
