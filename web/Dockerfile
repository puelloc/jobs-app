# The SvelteKit UI, built to a standalone Node server with adapter-node.
# Mirrors the rss-app ui image: build with node, run with node-slim.
FROM node:22 AS build
WORKDIR /src
COPY package.json package-lock.json ./
# Mount npm's cache so a lockfile change re-downloads only what actually changed. The build itself
# (vite) has no cross-run cache, so this is the only part that benefits.
RUN --mount=type=cache,target=/root/.npm npm ci
COPY . .
RUN npm run build

FROM node:22-slim
ENV NODE_ENV=production
WORKDIR /app
COPY --from=build /src/build ./build
COPY --from=build /src/package.json ./package.json
COPY --from=build /src/node_modules ./node_modules
# /api/* is proxied to the Go API by src/routes/api/[...path]/+server.js; the
# backend URL comes from the JOBS_API env (default http://127.0.0.1:8080).
EXPOSE 8080
CMD ["node", "build/index.js"]
