FROM gcr.io/distroless/static:nonroot
ARG TARGETOS
ARG TARGETARCH
COPY ./dist/spegel_${TARGETOS}_${TARGETARCH}/spegel /
USER root:root
ENTRYPOINT ["/spegel"]
