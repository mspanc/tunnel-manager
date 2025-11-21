FROM golang:1.25-alpine3.22 AS builder
WORKDIR /app
COPY . .
RUN go mod download && go build -o tunnel-manager cmd/tunnel-manager/main.go

FROM alpine:3.22
RUN apk add --no-cache curl ca-certificates
COPY --from=builder /app/tunnel-manager /usr/local/bin/tunnel-manager
CMD ["/usr/local/bin/tunnel-manager"]
