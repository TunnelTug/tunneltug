# syntax=docker/dockerfile:1

FROM golang:1.25-alpine AS builder
RUN apk add --no-cache git ca-certificates
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-s -w -X main.Version=${VERSION} -X main.Commit=${COMMIT} -X main.BuildDate=${BUILD_DATE}" \
    -o /out/tunneltug .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/tunneltug /usr/local/bin/tunneltug
USER nonroot:nonroot
EXPOSE 80/tcp 443/tcp 443/udp 8080/tcp 9000/udp
ENTRYPOINT ["/usr/local/bin/tunneltug"]