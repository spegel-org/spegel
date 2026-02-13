FROM harbor.infini-ai.com/share/alpine:3.20
ARG TARGETOS
ARG TARGETARCH
COPY ./dist/spegel_${TARGETOS}_${TARGETARCH}/spegel /
USER root:root
ENTRYPOINT ["/spegel"]
