FROM --platform=$BUILDPLATFORM golang:1.27.2-alpine3.24@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
ARG TARGETOS=linux
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /out/tado-bridge ./cmd/tado-bridge
RUN mkdir -p /out/credentials

FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
COPY --from=build /out/tado-bridge /tado-bridge
COPY --from=build --chown=65532:65532 /out/credentials/ /var/lib/tado-bridge/
COPY LICENSE /LICENSE
USER 65532:65532
ENV TADO_CREDENTIALS_FILE=/var/lib/tado-bridge/credentials.json
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 CMD ["/tado-bridge", "healthcheck"]
ENTRYPOINT ["/tado-bridge"]
CMD ["serve"]
