# Benchmark image: the pinned toolchain (its libvips and ffmpeg), both Go binaries, the Rust sources
# (built on the Machine by run), the reference load generator and the parity seed.
FROM once-campfire-go:toolchain
COPY go /bench/go
COPY rust /bench/rust
COPY campfire-main /bench/campfire-main
COPY shims /bench/shims
COPY run /bench/run
ENV PATH=/bench/shims:$PATH HOME=/root
WORKDIR /bench/go
CMD ["/bench/run"]
