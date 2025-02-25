FROM golang:1.23.6@sha256:9e20c012e0f725d3c92492f47f02c7761e6a5abb64c97fb752f80fd8863e6b08 AS builder
RUN mkdir /build
WORKDIR /build
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download
COPY main.go main.go
COPY internal/ internal/
COPY pkg/ pkg/
RUN CGO_ENABLED=0 go build -installsuffix 'static' -o spegel .

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /build/spegel /app/
WORKDIR /app
USER root:root
ENTRYPOINT ["./spegel"]
