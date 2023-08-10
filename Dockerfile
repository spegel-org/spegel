FROM golang:1.21 as builder
RUN mkdir /build
WORKDIR /build
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download
COPY main.go main.go
COPY internal/ internal/
RUN CGO_ENABLED=0 go build -installsuffix 'static' -o spegel .

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /build/spegel /app/
WORKDIR /app
USER root:root
ENTRYPOINT ["./spegel"]
