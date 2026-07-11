FROM golang:1.25-trixie AS builder

ARG QQ_QUOTE_REPO=https://github.com/Penryn/qq-quote-generator.git
ARG QQ_QUOTE_REF=main

WORKDIR /src
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates git \
    && rm -rf /var/lib/apt/lists/*
RUN git clone --depth 1 --branch "${QQ_QUOTE_REF}" "${QQ_QUOTE_REPO}" .
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/qq-quote-go .

FROM alpine:3.19

RUN apk add --no-cache \
    ca-certificates \
    chromium \
    nss \
    freetype \
    harfbuzz \
    ttf-freefont \
    font-noto-cjk \
    tzdata

ENV ROD_BROWSER_BIN=/usr/bin/chromium-browser
ENV TZ=Asia/Shanghai
ENV POOL_SIZE=4
ENV PORT=5000

COPY --from=builder /out/qq-quote-go /usr/local/bin/qq-quote-go

EXPOSE 5000
ENTRYPOINT ["qq-quote-go"]
