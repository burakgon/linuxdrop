FROM oven/bun:1-alpine

WORKDIR /app

# Install deps first for better layer caching.
COPY package.json bun.lock* ./
RUN bun install --production

COPY tsconfig.json ./
COPY src ./src

# Writable data dir for the SQLite catch-up store.
RUN mkdir -p /data && chown -R bun:bun /data
VOLUME /data

ENV PORT=3000
ENV DB_PATH=/data/bgnconnect.db
EXPOSE 3000
USER bun

CMD ["bun", "src/index.ts"]
