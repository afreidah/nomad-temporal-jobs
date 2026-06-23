FROM --platform=$BUILDPLATFORM golang:1.26.4 AS godoc
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
# Lock-file-enforced install: the version is pinned by the `tool` directive in
# go.mod (+ go.sum), so no floating @version on the command line.
RUN go install github.com/princjef/gomarkdoc/cmd/gomarkdoc
# Whole tree so `go list ./...` in gen-godoc.sh sees every package; the package
# set is discovered dynamically, never hardcoded here.
COPY . .
RUN sh web/gen-godoc.sh /godoc

# Pinned by digest for reproducible builds (bump via renovate/dependabot).
FROM --platform=$BUILDPLATFORM hugomods/hugo@sha256:367ea85950c9f3e12746fbf049f29ea00758c86791072dfdc9772748ff9306bc AS build
WORKDIR /src/web
COPY web/ /src/web/
COPY README.md /src/README.md
COPY CHANGELOG.md /src/CHANGELOG.md
COPY --from=godoc /godoc/ /src/web/content/godoc/
COPY web/content/godoc/_index.md /src/web/content/godoc/_index.md
RUN hugo --minify

FROM nginx:alpine
RUN apk upgrade --no-cache
COPY --from=build /src/web/public /usr/share/nginx/html
EXPOSE 80
