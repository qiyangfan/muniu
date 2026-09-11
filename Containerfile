# muniu dev 镜像:免拉取形态——宿主交叉编译的 linux/arm64 静态二进制 + scratch 运行层。
# 不从任何 registry 拉基础镜像(make dev-build 已产出 dist/muniu-linux-arm64),
# 与单二进制哲学一致;HTTPS 出站根证书由 make dev-up 挂载宿主 cert.pem + SSL_CERT_FILE 提供。
FROM scratch
COPY dist/muniu-linux-arm64 /usr/local/bin/muniu
WORKDIR /app
ENTRYPOINT ["/usr/local/bin/muniu"]
