FROM rust:1.97-slim-trixie AS resvg-builder

ARG QQ_QUOTE_REPO=https://github.com/SugarMGP/qq-quote-generator.git
ARG QQ_QUOTE_REF=main

RUN apt-get update && apt-get install -y --no-install-recommends \
    git \
    build-essential \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src
RUN git clone --depth 1 --branch "${QQ_QUOTE_REF}" "${QQ_QUOTE_REPO}" .
RUN sh scripts/build-resvg.sh

FROM golang:1.25-trixie AS go-builder

ARG GOPROXY=https://goproxy.cn,direct

RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    libunwind-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY --from=resvg-builder /src/ ./
RUN GOPROXY="${GOPROXY}" go mod download

RUN CGO_LDFLAGS="$(cat internal/resvg/lib/linux-amd64/native-static-libs.txt)" \
    CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/qq-quote-go .

FROM debian:trixie-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    fontconfig \
    fonts-noto-cjk \
    libunwind8 \
    tzdata \
    && rm -rf /var/lib/apt/lists/*

ENV PORT=5000
ENV TZ=Asia/Shanghai

COPY --from=go-builder /out/qq-quote-go /usr/local/bin/qq-quote-go

EXPOSE 5000
ENTRYPOINT ["qq-quote-go"]