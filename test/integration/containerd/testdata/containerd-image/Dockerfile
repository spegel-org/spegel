FROM ubuntu:25.04
RUN apt update && \
    apt install -y wget tar ca-certificates && \
    apt clean && \
    rm -rf /var/lib/apt/lists/*
ARG TARGETARCH
ARG CONTAINERD_VERSION
RUN wget -qO- https://github.com/containerd/containerd/releases/download/v${CONTAINERD_VERSION}/containerd-${CONTAINERD_VERSION}-linux-${TARGETARCH}.tar.gz | tar -xz -C /usr/local/bin --strip-components=1
COPY config.toml /etc/containerd/config.toml
COPY entrypoint.sh entrypoint.sh
ENTRYPOINT ["./entrypoint.sh"]
