# syntax=docker/dockerfile:1.7
ARG ALPINE_IMAGE

FROM --platform=$BUILDPLATFORM ${ALPINE_IMAGE} AS fetch
RUN apk add --no-cache ca-certificates coreutils curl tar
WORKDIR /src
COPY runner/artifacts/ajiasu-4.2.3.0.env runner/artifacts/ajiasu-4.2.3.0.env
COPY runner/scripts/fetch-ajiasu.sh runner/scripts/fetch-ajiasu.sh
ARG TARGETARCH
RUN runner/scripts/fetch-ajiasu.sh "$TARGETARCH" /out

FROM ${ALPINE_IMAGE}
LABEL org.opencontainers.image.title="AJiaSu Runner" \
      org.opencontainers.image.version="4.2.3.0" \
      org.opencontainers.image.description="Verified isolated runner for the official AJiaSu Linux CLI"
RUN addgroup -g 65532 -S runner && adduser -S -D -H -u 65532 -G runner runner \
    && apk add --no-cache ca-certificates \
    && mkdir -p /run/ajiasu /var/lib/ajiasu \
    && ln -s /run/ajiasu/ajiasu.conf /etc/ajiasu.conf \
    && chown -R 65532:65532 /run/ajiasu /var/lib/ajiasu
COPY --from=fetch --chown=65532:65532 /out/ajiasu /usr/local/bin/ajiasu
COPY --chown=65532:65532 runner/bin/runner-entrypoint.sh /usr/local/bin/runner-entrypoint.sh
USER 65532:65532
WORKDIR /var/lib/ajiasu
ENTRYPOINT ["/usr/local/bin/runner-entrypoint.sh"]
CMD ["connect"]
