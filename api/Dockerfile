FROM golang:1.26.8-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/server ./cmd/server && \
    CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/worker ./cmd/worker && \
    CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/providercheck ./cmd/providercheck && \
    CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/probe ./cmd/probe && \
    mkdir -p /spool/uploads && chmod 700 /spool/uploads && chown -R 10001:10001 /spool
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ /app/
COPY --from=build --chown=10001:10001 /spool/ /var/lib/secureshare/
USER 10001:10001
ENV PORT=8080 UPLOAD_TEMP_DIR=/var/lib/secureshare/uploads
EXPOSE 8080
CMD ["/app/server"]
