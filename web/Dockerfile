# The web client as a static site, served by nginx on port 8080.
#
#   docker build -t remote-agent-web web
#   docker run --rm -p 8080:8080 remote-agent-web
#
# rad answers a page only from the origins in its config's web_origins, so add
# the address you open this at there, such as "my-server.local:8080".

# The build runs on the builder's own platform: its output is the same for
# every platform the image is for.
FROM --platform=$BUILDPLATFORM node:24-trixie-slim AS build
WORKDIR /web
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
RUN npm run build

FROM nginxinc/nginx-unprivileged:1.29-alpine
COPY docker/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /web/dist /usr/share/nginx/html
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO /dev/null http://127.0.0.1:8080/ || exit 1
