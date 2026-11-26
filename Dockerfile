FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/server ./cmd/server

# Image for the azure and anthropic providers (default target).
FROM gcr.io/distroless/static-debian12:nonroot AS server
COPY --from=build /out/server /server
ENV HTTP_ADDR=0.0.0.0:8080
EXPOSE 8080
ENTRYPOINT ["/server"]

# Image for LLM_PROVIDER=copilot: adds Node and the Copilot CLI.
# Build with: docker build --target with-copilot .
FROM node:22-slim AS with-copilot
RUN npm install -g @github/copilot && useradd -m app
COPY --from=build /out/server /usr/local/bin/server
USER app
ENV HTTP_ADDR=0.0.0.0:8080
EXPOSE 8080
CMD ["server"]

FROM server
