FROM debian:13

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        build-essential \
        cmake \
        ninja-build \
        clang \
        pkg-config \
        gdb \
        python3 \
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
        libasound2 \
        libatk-bridge2.0-0 \
        libatk1.0-0 \
        libatspi2.0-0 \
        libcairo2 \
        libcups2 \
        libdbus-1-3 \
        libegl1 \
        libfontconfig1 \
        libgbm1 \
        libgl1 \
        libgles2 \
        libgtk-3-0 \
        libnss3 \
        libnspr4 \
        libpango-1.0-0 \
        libx11-6 \
        libx11-xcb1 \
        libxcb1 \
        libxcomposite1 \
        libxcursor1 \
        libxdamage1 \
        libxext6 \
        libxfixes3 \
        libxkbcommon0 \
        libxi6 \
        libxrandr2 \
        libxrender1 \
        libxss1 \
        libxtst6 \
        xdg-utils \
        dbus \
        libgstreamer1.0-0 \
        libgstreamer-plugins-base1.0-0 \
        gstreamer1.0-plugins-base \
        gstreamer1.0-plugins-good \
        gstreamer1.0-plugins-bad \
        gstreamer1.0-libav \
        gstreamer1.0-x \
        gstreamer1.0-alsa \
        gstreamer1.0-pulseaudio \
    && extrepo enable mise \
    && apt-get update \
    && apt-get install -y --no-install-recommends mise \
    && rm -rf /var/lib/apt/lists/*

COPY docker/xdg-open /usr/local/bin/xdg-open
RUN ln -sf /usr/bin/xdg-open /usr/local/bin/xdg-open.real \
    && chmod +x /usr/local/bin/xdg-open
