FROM golang:1.27.3-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags='-s -w' -o /out/pingone-promote ./cmd/pingone-promote

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && addgroup -S -g 10001 pingone && adduser -S -D -H -u 10001 -G pingone pingone
COPY --from=build --chown=10001:10001 /out/pingone-promote /usr/local/bin/pingone-promote
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/pingone-promote"]
