FROM debian:bookworm-slim

ARG CONTAINERD_VERSION=2.2.1
ARG CRICTL_VERSION=v1.31.1
ARG DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      systemd \
      systemd-sysv \
      ca-certificates \
      curl \
      gnupg \
      iproute2 \
 && install -m 0755 -d /etc/apt/keyrings \
 && curl -fsSL https://download.docker.com/linux/debian/gpg \
      | gpg --dearmor -o /etc/apt/keyrings/docker.gpg \
 && chmod a+r /etc/apt/keyrings/docker.gpg \
 && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] \
      https://download.docker.com/linux/debian bookworm stable" \
      > /etc/apt/sources.list.d/docker.list \
 && apt-get update \
 && apt-get install -y --no-install-recommends \
      "containerd.io=${CONTAINERD_VERSION}*" \
 && ARCH=$(dpkg --print-architecture) \
 && curl -fsSL "https://github.com/kubernetes-sigs/cri-tools/releases/download/${CRICTL_VERSION}/crictl-${CRICTL_VERSION}-linux-${ARCH}.tar.gz" \
      | tar -C /usr/local/bin -xz \
 && printf 'runtime-endpoint: unix:///run/containerd/containerd.sock\nimage-endpoint: unix:///run/containerd/containerd.sock\ntimeout: 30\n' > /etc/crictl.yaml \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/*

RUN find /etc/systemd/system /lib/systemd/system -path '*.wants/*' \
      ! -name 'systemd-tmpfiles-setup.service' \
      ! -name 'systemd-tmpfiles-setup-dev.service' \
      ! -name 'systemd-journald.service' \
      ! -name 'systemd-journal-flush.service' \
      ! -name 'dbus.service' \
      -delete \
 && rm -f /lib/systemd/system/multi-user.target.wants/* \
          /etc/systemd/system/*.wants/* \
          /lib/systemd/system/local-fs.target.wants/* \
          /lib/systemd/system/sockets.target.wants/*udev* \
          /lib/systemd/system/sockets.target.wants/*initctl* \
          /lib/systemd/system/basic.target.wants/* \
          /lib/systemd/system/anaconda.target.wants/*

COPY containerd-config.toml /etc/containerd/config.toml
COPY spegel.service /etc/systemd/system/spegel.service

RUN mkdir -p /etc/spegel \
 && printf 'SPEGEL_HTTP_BOOTSTRAP_PEER=http://127.0.0.1:5002\n' > /etc/spegel/peer.env \
 && systemctl enable containerd.service spegel.service

STOPSIGNAL SIGRTMIN+3

CMD ["/lib/systemd/systemd"]
