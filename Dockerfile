FROM mcr.microsoft.com/oss/go/microsoft/golang:1.23.2 AS builder
RUN mkdir /build
WORKDIR /build
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download
COPY main.go main.go
COPY internal/ internal/
COPY pkg/ pkg/
RUN CGO_ENABLED=0 go build -installsuffix 'static' -o spegel .

FROM mcr.microsoft.com/mirror/gcr/distroless/static:nonroot
COPY --from=builder /build/spegel /app/
WORKDIR /app
USER root:root
ENTRYPOINT ["./spegel"]