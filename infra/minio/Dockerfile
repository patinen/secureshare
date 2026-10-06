# Official community images are no longer available from the registries used
# during validation. Build pinned upstream releases, not an unofficial mirror.
FROM golang:1.26-bookworm AS build
ENV CGO_ENABLED=0
RUN git clone --depth 1 --branch RELEASE.2025-09-07T16-13-09Z https://github.com/minio/minio.git /src/minio
WORKDIR /src/minio
RUN go build -trimpath -o /out/minio .
RUN git clone --depth 1 --branch RELEASE.2025-08-13T08-35-41Z https://github.com/minio/mc.git /src/mc
WORKDIR /src/mc
RUN go build -trimpath -o /out/mc .

FROM debian:bookworm-slim
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/minio /out/mc /usr/local/bin/
RUN mkdir /data && chown 10001:10001 /data
ENV HOME=/tmp
USER 10001:10001
EXPOSE 9000 9001
ENTRYPOINT ["minio"]
