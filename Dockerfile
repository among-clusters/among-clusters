FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN export CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" && go build -trimpath -ldflags='-s -w' -o /out/hub ./cmd/hub && go build -trimpath -ldflags='-s -w' -o /out/agent ./cmd/agent && go build -trimpath -ldflags='-s -w' -o /out/gateway ./cmd/gateway && go build -trimpath -ldflags='-s -w' -o /out/broker ./cmd/broker && go build -trimpath -ldflags='-s -w' -o /out/migrate ./cmd/migrate
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/hub /hub
COPY --from=build /out/agent /agent
COPY --from=build /out/gateway /gateway
COPY --from=build /out/broker /broker
COPY --from=build /out/migrate /migrate
USER 65532:65532
ENTRYPOINT ["/hub"]
