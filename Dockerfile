# ============================================================
# 阶段 1：编译
# ============================================================
FROM golang:1.26-alpine AS builder

WORKDIR /app

# 先只拷 go.mod，利用 Docker 层缓存：以后改代码不会重下依赖
COPY go.mod ./

# go:embed 的 web/ 必须在编译阶段就位，否则 build 直接报
# "pattern web/index.html: no matching files found"。
# 所以这几个 COPY 只能放在 RUN go build 之前。
COPY *.go ./
COPY web/ ./web/

# CGO_ENABLED=0 静态编译，产物可以直接丢进 alpine
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o proxyip .

# ============================================================
# 阶段 2：运行
# ============================================================
FROM alpine:3.21

# ca-certificates 不能省：精简镜像默认没有根证书，
# 少了它所有 HTTPS 测速目标都会报 TLS 证书错误（而这个工具只测 HTTPS）
# tzdata 用来让日志时间戳是本地时区
RUN apk add --no-cache ca-certificates tzdata

ENV TZ=Asia/Shanghai

WORKDIR /app

COPY --from=builder /app/proxyip .

# 端口可用 PROXYIP_PORT 覆盖
EXPOSE 8899

CMD ["./proxyip"]
