FROM golang:1.21.4@sha256:337543447173c2238c78d4851456760dcc57c1dfa8c3bcd94cbee8b0f7b32ad0 as builder
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
