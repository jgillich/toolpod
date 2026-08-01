FROM debian:13

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        build-essential \
        cmake \
        ninja-build \
        clang \
        pkg-config \
        gdb \
        libssl-dev \
        libcurl4-openssl-dev \
        zlib1g-dev \
        libreadline-dev \
        libffi-dev \
        libsqlite3-dev \
        autoconf \
        automake \
        libtool \
        git \
        curl \
        wget \
        ca-certificates \
        extrepo \
    && extrepo enable mise \
    && apt-get update \
    && apt-get install -y --no-install-recommends mise \
    && rm -rf /var/lib/apt/lists/*
