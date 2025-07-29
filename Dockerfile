# Build stage
FROM golang:1.24-bullseye AS builder

WORKDIR /mcp

COPY  . .

RUN go mod download

RUN go build -o mcpserver ./server

# Final stage

FROM cgr.dev/chainguard/wolfi-base

RUN apk update && apk add git postgresql python-3.12 py3.12-pip py3.12-setuptools sqlite 

WORKDIR /app

COPY --from=builder /mcp/mcpserver /app/

EXPOSE 1234

ENTRYPOINT ["/app/mcpserver"]



