FROM golang:1.27.0-alpine AS build
ARG MODULE=backend
WORKDIR /src
COPY backend/go.mod backend/go.sum backend/
COPY mocks/go.mod mocks/go.sum mocks/
COPY backend/ backend/
COPY mocks/ mocks/
RUN cd "$MODULE" && CGO_ENABLED=0 go build -trimpath -o /out/server ./cmd/server

FROM alpine:3.24
COPY --from=build /out/server /usr/local/bin/server
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/server"]
