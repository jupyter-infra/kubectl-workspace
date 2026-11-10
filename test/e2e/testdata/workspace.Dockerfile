# A stand-in workspace image: the published SSH server binary in a Debian base with a
# shell and a user, the way a workspace image embeds it. The server is the container's
# main process and binds loopback, as it does in a workspace pod.
FROM ghcr.io/jupyter-infra/remote-access-server:v0.1.0-rc.2 AS carrier
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends bash ca-certificates && \
    apt-get clean && rm -rf /var/lib/apt/lists/*
RUN useradd -m -s /bin/bash jovyan
COPY --from=carrier /remote-access-server /usr/local/bin/remote-access-server
USER jovyan
WORKDIR /home/jovyan
ENV HOME=/home/jovyan
ENTRYPOINT ["/usr/local/bin/remote-access-server", "--port", "2222"]
