# goreleaser (dockers_v2) builds the binary outside this image and lays the
# context out per platform: the binary sits at $TARGETPLATFORM/sentra
# (e.g. linux/amd64/sentra), which is how one Dockerfile serves the
# multi-arch buildx build. `sentra` is a static, CGO-disabled binary so a
# distroless runtime is fine.
FROM gcr.io/distroless/static-debian12:nonroot

ARG TARGETPLATFORM
COPY $TARGETPLATFORM/sentra /usr/local/bin/sentra

USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/sentra"]
