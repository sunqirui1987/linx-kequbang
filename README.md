# Linx Kequbang OpenAI API 适配器

这是一个 OpenAI API 协议 (`/v1/chat/completions`) 的 Go 语言实现，用于将请求桥接到 Kequbang WebSocket API。

## 功能特性
- **OpenAI 兼容接口**: 暴露 `/v1/chat/completions` 接口。
- **完整协议支持**: 完美支持 **流式 (Stream)** 和 **非流式 (Non-Stream)** 两种模式，响应格式严格遵循 OpenAI 规范。
- **WebSocket 连接池**: 复用上游 WebSocket 连接，自动管理心跳和断线重连。
- **结构化日志**: 集成 Zap 日志库，提供详细的请求追踪和错误上下文。
- **鉴权认证**: 将 HTTP 请求中的 `Authorization` 头 (Bearer token) 传递给 WebSocket 握手。

## 使用方法

### 1. 构建
```bash
go build .
```

### 2. 运行
```bash
./linx-kequbang
```
服务器将在 `8080` 端口启动。

### 3. 发送请求
请将 `<YOUR_TOKEN>` 替换为你的实际 License Key。 ckey20251119

**流式请求 (推荐):**
```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <YOUR_TOKEN>" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [
      {"role": "user", "content": "你好"}
    ],
    "stream": true
  }'
```

**非流式请求:**
```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <YOUR_TOKEN>" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [
      {"role": "user", "content": "你好"}
    ]
  }'
```

## 协议详情
适配器根据 `对接文档.txt` 实现了以下流程：
1. **连接**: 带有 Authorization 头的 WebSocket 握手。
2. **启动**: 发送 `type: start`，包含随机生成的 `dialogId` 和 `userId` UUID。
3. **开始语音**: 发送 `type: startSpeech`。
4. **发送消息**: 发送 `type: sendSpeechText` 以及用户的输入内容。
5. **结束语音**: 发送 `type: stopSpeech`。
6. **接收**: 监听 `type: text` 数据块，直到收到 `type: playOver`。
