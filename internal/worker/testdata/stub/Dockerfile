FROM ubuntu:24.04

RUN apt-get update -qq \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq curl ca-certificates \
 && rm -rf /var/lib/apt/lists/*

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
