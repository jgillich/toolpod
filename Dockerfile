FROM debian:13

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        extrepo \
    && rm -rf /var/lib/apt/lists/*
