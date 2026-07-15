# syntax=docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e
ARG ALPINE_IMAGE

FROM --platform=$BUILDPLATFORM ${ALPINE_IMAGE} AS fetch
# Supply-chain locks are updated only through reviewed rebuilds.
RUN apk add --no-cache \
    ca-certificates=20260611-r0 \
    coreutils=9.7-r1 \
    curl=8.14.1-r3 \
    tar=1.35-r3
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
    && apk add --no-cache ca-certificates=20260611-r0 \
    && mkdir -p /run/ajiasu /var/lib/ajiasu \
    && ln -s /run/ajiasu/ajiasu.conf /etc/ajiasu.conf \
    && chown -R 65532:65532 /run/ajiasu /var/lib/ajiasu
COPY --from=fetch --chmod=0555 /out/ajiasu /usr/local/bin/ajiasu
COPY --chmod=0555 runner/bin/runner-entrypoint.sh /usr/local/bin/runner-entrypoint.sh
USER 65532:65532
WORKDIR /var/lib/ajiasu
ENTRYPOINT ["/usr/local/bin/runner-entrypoint.sh"]
CMD ["connect"]
