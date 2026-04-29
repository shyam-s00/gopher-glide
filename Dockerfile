# ── Dockerfile for ghcr.io/shyam-s00/gopher-glide ───────────────────────────
# GoReleaser builds the binary for linux/amd64 before invoking Docker,
# then copies it straight in — no Go toolchain needed inside the image.
# This keeps the final image as small as possible.

FROM gcr.io/distroless/static-debian12:nonroot

# OCI labels (also stamped by GoReleaser build_flag_templates)
LABEL org.opencontainers.image.title="gg" \
      org.opencontainers.image.description="Gopher Glide — HTTP load-testing tool" \
      org.opencontainers.image.url="https://github.com/shyam-s00/gopher-glide" \
      org.opencontainers.image.source="https://github.com/shyam-s00/gopher-glide" \
      org.opencontainers.image.licenses="MIT"

# GoReleaser places the pre-built linux/amd64 binary in the build context
COPY gg /gg

ENTRYPOINT ["/gg"]

