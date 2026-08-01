FROM debian:13

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        git \
        extrepo \
    && extrepo enable mise \
    && apt-get update \
    && apt-get install -y --no-install-recommends mise \
    && rm -rf /var/lib/apt/lists/*

COPY docker/xdg-open /usr/local/bin/xdg-open
RUN ln -sf /usr/bin/xdg-open /usr/local/bin/xdg-open.real \
    && chmod +x /usr/local/bin/xdg-open
