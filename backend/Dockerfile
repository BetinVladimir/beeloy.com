# Build context: the repository root (needs backend/ and migrations/).
FROM golang:1.26 AS build
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags "-s -w" -o /out/api ./cmd/api \
 && go build -trimpath -ldflags "-s -w" -o /out/content-sync ./cmd/content-sync \
 && go build -trimpath -ldflags "-s -w" -o /out/migrate ./cmd/migrate \
 && go build -trimpath -ldflags "-s -w" -o /out/translator ./cmd/translator

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/api /out/content-sync /out/migrate /out/translator /
COPY migrations /migrations
COPY contracts /contracts
ENV MIGRATIONS_DIR=/migrations
USER nonroot:nonroot
EXPOSE 8080 8081 9090
# the command is chosen by docker compose: ["/api"], ["/content-sync","run"], ["/migrate","up"]
CMD ["/api"]
