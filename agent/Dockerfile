# --- build stage ---
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/ntm-agent .

# --- runtime stage ---
# nmap is the standard discovery tool the agent orchestrates for authorized
# asset inventory of the operator's own network.
FROM alpine:3.20
RUN apk add --no-cache nmap nmap-scripts ca-certificates
WORKDIR /app
COPY --from=build /out/ntm-agent /app/ntm-agent
RUN mkdir -p /var/lib/ntm
ENTRYPOINT ["/app/ntm-agent"]
