# Used by goreleaser: the kvx binary is built outside and copied in.
FROM gcr.io/distroless/static-debian12:nonroot
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/kvx /usr/local/bin/kvx
ENTRYPOINT ["/usr/local/bin/kvx"]
