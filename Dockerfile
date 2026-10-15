FROM golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY conformance ./conformance
RUN test -z "$(gofmt -l cmd internal)" && go vet ./...
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/worker ./cmd/worker && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/healthcheck ./cmd/healthcheck

FROM scratch AS runtime
COPY --from=build /out/ /app/
USER 65532:65532
ENV HTTP_ADDR=:8080
EXPOSE 8080
ENTRYPOINT ["/app/api"]

FROM build AS integration-test
ENTRYPOINT ["go", "test", "-v", "./internal/platform/adapters/mysql", "./internal/identity/...", "./internal/integration/...", "./internal/audit/..."]
