# BookSmith 多阶段构建：前端 → 后端 → 运行镜像
# 构建上下文必须是仓库根目录（frontend/ 与 backend/ 都在其中）

# ---------- 阶段 1：前端构建 ----------
FROM node:22-alpine AS frontend
WORKDIR /app/frontend
COPY frontend/package.json frontend/package-lock.json* ./
RUN npm install --no-audit --no-fund
COPY frontend/ ./
# vite outDir 指向 ../backend/web/dist
RUN npm run build

# ---------- 阶段 2：后端构建（内嵌前端产物） ----------
FROM golang:1.26-alpine AS backend
WORKDIR /app/backend
# 依赖先行（利用层缓存）
COPY backend/go.mod backend/go.sum* ./
RUN go mod download
COPY backend/ ./
# 前端产物拷入 embed 目录
COPY --from=frontend /app/backend/web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w" -o /booksmith ./cmd/booksmith/

# ---------- 阶段 3：运行镜像 ----------
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S booksmith && adduser -S booksmith -G booksmith
WORKDIR /app
COPY --from=backend /booksmith /app/booksmith
# 数据目录（书籍缓存 / 导出文件 / SQLite）
RUN mkdir -p /data/downloads && chown -R booksmith:booksmith /data
USER booksmith
ENV BOOKSMITH_DATA=/data
EXPOSE 18500
VOLUME ["/data"]
ENTRYPOINT ["/app/booksmith"]
CMD ["--data", "/data", "--addr", "0.0.0.0:18500"]