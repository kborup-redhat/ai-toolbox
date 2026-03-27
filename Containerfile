ARG BUILDER_IMAGE=registry.redhat.io/ubi9/go-toolset:latest
ARG RUNTIME_IMAGE=registry.access.redhat.com/ubi9-minimal:latest

FROM ${BUILDER_IMAGE} AS builder
ARG APP_VERSION=dev
WORKDIR /opt/app-root/src
COPY . .
RUN go build -ldflags="-X main.version=${APP_VERSION}" -o ai-toolbox .
RUN go build -o loadtest-runner ./cmd/loadtest-runner

FROM ${RUNTIME_IMAGE}
COPY --from=builder /opt/app-root/src/ai-toolbox /usr/local/bin/ai-toolbox
COPY --from=builder /opt/app-root/src/loadtest-runner /usr/local/bin/loadtest-runner
USER 1001
EXPOSE 8080
ENTRYPOINT ["ai-toolbox"]
