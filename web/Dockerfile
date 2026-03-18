FROM --platform=$BUILDPLATFORM golang:1.26.1 AS godoc
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY shared/ shared/
COPY backup/ backup/
COPY trivyscan/ trivyscan/
COPY nodecleanup/ nodecleanup/
COPY cmd/ cmd/
RUN go install github.com/princjef/gomarkdoc/cmd/gomarkdoc@latest
RUN mkdir -p /godoc && \
    for entry in "shared:./shared" "backup-activities:./backup/activities" "backup-workflows:./backup/workflows" "trivyscan-activities:./trivyscan/activities" "trivyscan-workflows:./trivyscan/workflows" "nodecleanup-activities:./nodecleanup/activities" "nodecleanup-workflows:./nodecleanup/workflows"; do \
        name="${entry%%:*}"; pkg="${entry#*:}"; \
        printf -- '---\ntitle: "%s"\n---\n\n' "$name" > /godoc/$name.md && \
        gomarkdoc $pkg >> /godoc/$name.md && \
        sed -i '0,/^# /{/^# /d}' /godoc/$name.md; \
    done

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
