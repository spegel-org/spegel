FROM golang:1.24.1@sha256:52ff1b35ff8de185bf9fd26c70077190cd0bed1e9f16a2d498ce907e5c421268 AS builder
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
