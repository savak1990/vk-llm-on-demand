# Ubuntu 24.04 with curl and ca-certificates preinstalled: the build pulls
# one image and runs no apt-get, which hung on CI runner networks.
FROM buildpack-deps:noble-curl@sha256:6b03a2e5a50d3b482003a040045952cfbd7ed8081e1d6ac85f283b6e44fbff27

# Shadow apt-get so onstart.sh's opportunistic aria2c install fails
# instantly instead of waiting out a DNS/connect timeout with no network.
RUN printf '#!/usr/bin/env bash\nexit 1\n' > /usr/bin/apt-get && chmod +x /usr/bin/apt-get

COPY src.bin /src.bin
COPY llama-server /app/llama-server
RUN chmod +x /app/llama-server
COPY onstart.sh /onstart.sh
RUN chmod +x /onstart.sh
COPY wrapper.sh /wrapper.sh
RUN chmod +x /wrapper.sh

ENTRYPOINT ["/wrapper.sh"]
