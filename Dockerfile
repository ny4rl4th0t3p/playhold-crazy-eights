# Build from the repository's root:
#   docker build --target server .

FROM node:24-alpine AS page
# The TypeScript SDK installs from its GitHub repository, which npm fetches with git.
RUN apk add --no-cache git
WORKDIR /src
COPY web/package.json web/package-lock.json web/
RUN npm --prefix web ci
COPY web web
RUN npm --prefix web run build

FROM golang:1.27.4-alpine AS binaries
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=0
RUN go build -trimpath -o /out/server ./server
RUN go build -trimpath -o /out/bot ./bot
# The crash journal's folder. A new volume mounted there takes this owner, so the server can write to it.
RUN mkdir -p /out/journal

FROM scratch AS server
COPY --from=binaries /out/server /server
COPY --from=page /src/web/dist /web
COPY --from=binaries --chown=65534:65534 /out/journal /var/lib/cards
USER 65534
EXPOSE 8080
ENTRYPOINT ["/server", "-web", "/web"]

FROM scratch AS bot
COPY --from=binaries /out/bot /bot
USER 65534
ENTRYPOINT ["/bot"]