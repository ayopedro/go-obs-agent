FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/server ./cmd/server

FROM node:22-slim
RUN npm install -g @github/copilot && useradd -m app
COPY --from=build /out/server /usr/local/bin/server
USER app
ENV HTTP_ADDR=0.0.0.0:8080
EXPOSE 8080
CMD ["server"]
