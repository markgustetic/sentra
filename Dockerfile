# goreleaser builds the binary outside this image and copies it in.
# `sentra` is a static, CGO-disabled binary so a distroless runtime is fine.
FROM gcr.io/distroless/static-debian12:nonroot

COPY sentra /usr/local/bin/sentra

USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/sentra"]
