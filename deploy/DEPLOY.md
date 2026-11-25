# 授权码后台：云端部署指南

## 先说结论

后台本来就是一个独立程序，**不需要在每台客户电脑上跑**。现在的 `127.0.0.1:8090`
只是因为它恰好运行在你自己的电脑上。要变成云端模式，做三件事：

1. 把 `license-admin` 部署到服务器（本文档）
2. 给域名配 HTTPS（客户端只接受 `https://`）
3. **重新编译客户端**，把服务器地址和令牌烧进去（关键一步，见第 5 节）

第 3 步是必须的：客户拿到的 exe 里没有服务器地址，就找不到后台。

## 1. 准备

| 项目 | 要求 |
| --- | --- |
| 服务器 | 1 核 1G 足够，Ubuntu 20.04+ / Debian 11+ |
| 域名 | 例如 `license.yourdomain.com`，A 记录指向服务器 IP |
| 端口 | 放行 80（证书验证）和 443 |
| 软件 | Caddy（推荐，自动 HTTPS）或 Nginx + certbot |

域名解析生效后再继续，否则证书申请会失败。

## 2. 上传文件

把下面三个文件放到服务器同一个目录（例如 `/root/deploy`）：

```
license-admin-linux-amd64      # 后台程序
install.sh                     # 安装脚本
license-admin.service          # systemd 服务
Caddyfile                      # 反向代理配置
```

```bash
scp license-admin-linux-amd64 install.sh license-admin.service Caddyfile root@你的服务器:/root/deploy/
```

## 3. 一键安装

```bash
ssh root@你的服务器
cd /root/deploy
chmod +x install.sh
./install.sh license.yourdomain.com
```

脚本会：

- 创建 `license` 用户和 `/opt/license-admin`（数据放在 `/opt/license-admin/data`）
- 生成随机的管理员密码和接口令牌，写入 `/etc/license-admin/env`
- 注册并启动 systemd 服务（只监听 `127.0.0.1:8090`，不直接对外）
- 配好 Caddy 自动 HTTPS

结束时会把**后台地址、管理员密码、接口令牌**打印出来，请立即保存。

## 4. 验证

```bash
curl -s https://license.yourdomain.com/api/v1/pubkey
# 应返回 {"publicKey":"0f3c7e9a..."}
```

浏览器打开 `https://license.yourdomain.com/admin/`，用打印出的密码登录。

> 公钥必须与客户端内置的 `licensePublicKeyHex` 一致。如果不一致，说明
> `signing.key` 换了，客户端需要同步更新并重新编译。

## 5. 重新编译客户端（关键）

```bash
go build -ldflags "-s -w \
  -X main.defaultLicenseServer=https://license.yourdomain.com \
  -X main.defaultLicenseToken=安装脚本打印的令牌 \
  -X main.appVersion=0.1.0" \
  -o 海涛中控.exe .
```

这样客户拿到 exe 直接就能连后台，**不需要任何配置文件**。

仍然支持用 `config.json` 或环境变量覆盖（优先级更高），方便你调试：

```json
{
  "licenseServer": "https://license.yourdomain.com",
  "licenseToken": "令牌"
}
```

## 6. 数据与备份

`/opt/license-admin/data` 下三个文件：

| 文件 | 作用 | 丢失后果 |
| --- | --- | --- |
| `records.json` | 全部授权码与绑定记录 | 授权数据全丢 |
| `授权码.xlsx` | 导出的表格（每次变更重写） | 可从 records.json 重建 |
| `signing.key` | **签名私钥** | 所有已授权设备失效，需重新绑定 |

建议每天备份：

```bash
echo '0 3 * * * tar czf /root/backup/license-$(date +\%F).tgz -C /opt/license-admin data' | crontab -
```

`signing.key` 权限是 600，**不要提交到代码仓库，也不要发给任何人**。

## 7. 常见问题

**客户端提示「授权服务器地址必须使用 https://」**
说明 exe 里的默认地址是 http。重新按第 5 节编译。

**客户端提示「无法连接授权服务器」**
先 `curl https://你的域名/api/v1/pubkey`。不通就查：DNS、443 端口、`systemctl status caddy`、`systemctl status license-admin`。

**提示「授权服务器返回的签名无效」**
服务器 `signing.key` 与客户端内置公钥不匹配。要么把服务器上的 key 换成原来那把，要么重新编译客户端。

**提示「请求过于频繁」**
绑定接口限速每分钟 30 次/IP。批量装机时错开，或调整 `license-admin/ratelimit.go` 里的阈值。

**换了服务器 / 域名**
只需重新编译客户端；后台数据（records.json + signing.key）直接迁移过去即可，已授权设备不用重新绑定。

**要不要开 LICENSE_API_TOKEN？**
`install.sh` 默认就生成了。它防止别人拿到域名后刷接口把库存码烧光，建议保留。
令牌会编译进客户端，能被逆向提取，但足以挡住随手滥用。

## 8. 安全清单

- [ ] 后台只监听 `127.0.0.1`，对外一律走 HTTPS 反代
- [ ] `/admin/` 用 IP 白名单限制（Caddyfile / nginx 里有注释示例）
- [ ] `signing.key` 单独备份，权限 600
- [ ] 定期备份 `records.json`
- [ ] 不要把这套后台和客户端一起发给客户
