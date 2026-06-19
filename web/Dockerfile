FROM --platform=$BUILDPLATFORM golang:1.26.4 AS godoc
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
RUN go install github.com/princjef/gomarkdoc/cmd/gomarkdoc@latest
# Whole tree so `go list ./...` in gen-godoc.sh sees every package; the package
# set is discovered dynamically, never hardcoded here.
COPY . .
RUN sh web/gen-godoc.sh /godoc

FROM --platform=$BUILDPLATFORM hugomods/hugo:latest AS build
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
