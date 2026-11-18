FROM golang:1.25-alpine@sha256:1ae0735f00daffa3aaf1363a5184c0d2dc55c78e3db4ec70241cdac97bf84b59 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/jira-poc ./cmd/jira-poc

FROM build AS test
RUN apk add --no-cache gcc musl-dev
COPY examples ./examples
COPY testdata ./testdata
RUN --mount=type=secret,id=catalog,required=true CATALOG_FILE=/run/secrets/catalog CGO_ENABLED=1 go test -race ./...

FROM scratch AS runtime
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/jira-poc /jira-poc
USER 10001:10001
EXPOSE 8000
ENTRYPOINT ["/jira-poc"]
CMD ["serve"]
