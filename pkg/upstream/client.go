package upstream

import (
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"linx-kequbang/pkg/logger"
	"linx-kequbang/types"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

const (
	UpstreamWSURL   = "wss://ditui.kequbang.com/api-ws/v1/chat"
	HeartbeatPeriod = 10 * time.Second
	ReadTimeout     = 60 * time.Second
	WriteTimeout    = 10 * time.Second
)

// Client 封装了与上游服务的 WebSocket 连接
// Client wraps a websocket connection to the upstream service.
type Client struct {
	conn           *websocket.Conn
	token          string
	mu             sync.Mutex // 保护写操作 Protects write operations
	closeCh        chan struct{}
	isClosed       bool
	messageHandler func(types.WSMessage) // 当前活跃请求的回调 Callback for current active request
}

func tokenPreview(token string) string {
	if token == "" {
		return ""
	}
	const max = 10
	if len(token) <= max {
		return token
	}
	return token[:max] + "..."
}

func resetTimer(t *time.Timer, d time.Duration) {
	if t == nil {
		return
	}
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

// NewClient 创建一个新的上游连接
// NewClient creates a new connection to the upstream.
func NewClient(token string) (*Client, error) {
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}
	headers := http.Header{}
	headers.Add("Authorization", token) // Token 应该包含 "Bearer " 前缀

	conn, _, err := dialer.Dial(UpstreamWSURL, headers)
	if err != nil {
		logger.Error("WebSocket 连接失败", zap.Error(err), zap.String("url", UpstreamWSURL))
		return nil, fmt.Errorf("dial failed: %w", err)
	}

	conn.SetReadDeadline(time.Now().Add(ReadTimeout))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(ReadTimeout))
		return nil
	})

	c := &Client{
		conn:    conn,
		token:   token,
		closeCh: make(chan struct{}),
	}

	logger.Info("建立了新的 WebSocket 连接", zap.String("token_prefix", tokenPreview(token)))

	// 启动后台循环
	go c.readLoop()
	go c.heartbeatLoop()

	return c, nil
}

// Close 关闭连接
// Close closes the connection.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.isClosed {
		return
	}
	c.isClosed = true
	close(c.closeCh)
	c.conn.Close()
	logger.Info("WebSocket 连接已关闭")
}

// IsAlive 检查连接是否仍然有效
// IsAlive checks if the connection is likely still valid.
func (c *Client) IsAlive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.isClosed
}

// readLoop 持续从 WebSocket 读取消息
// readLoop continuously reads messages from the websocket.
func (c *Client) readLoop() {
	defer c.Close()
	c.conn.SetReadLimit(10 * 1024 * 1024) // 10MB limit

	for {
		c.conn.SetReadDeadline(time.Now().Add(ReadTimeout))
		var msg types.WSMessage
		err := c.conn.ReadJSON(&msg)
		if err != nil {
			c.mu.Lock()
			closed := c.isClosed
			c.mu.Unlock()
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure) && !closed {
				logger.Error("WS 读取错误", zap.Error(err))
			}
			return
		}

		// 处理心跳响应
		if msg.Type == types.MsgTypeHeartbeat {
			logger.Debug("收到心跳响应")
			continue
		}

		logger.Info("收到上游消息",
			zap.String("type", msg.Type),
			zap.String("dialog_id", msg.DialogID),
			zap.Int("content_len", len(msg.Content)),
			zap.String("content_preview", truncate(msg.Content, 50)),
			zap.Int("text_len", len(msg.Text)),
			zap.String("text_preview", truncate(msg.Text, 50)),
		)

		// 分发给活跃的处理器
		c.mu.Lock()
		handler := c.messageHandler
		c.mu.Unlock()

		if handler != nil {
			handler(msg)
		} else {
			logger.Debug("收到消息但无活跃处理器", zap.String("type", msg.Type))
		}
	}
}

// heartbeatLoop 定期发送心跳
// heartbeatLoop sends a heartbeat periodically.
func (c *Client) heartbeatLoop() {
	ticker := time.NewTicker(HeartbeatPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-c.closeCh:
			return
		case <-ticker.C:
			_ = c.WriteControl(websocket.PingMessage, nil)
			if err := c.WriteJSON(types.WSMessage{Type: types.MsgTypeHeartbeat}); err != nil {
				logger.Error("发送心跳失败", zap.Error(err))
				c.Close()
				return
			}
			logger.Debug("发送心跳成功")
		}
	}
}

// WriteJSON 安全地发送 JSON 消息
// WriteJSON sends a message safely.
func (c *Client) WriteJSON(v interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.isClosed {
		return errors.New("connection closed")
	}
	c.conn.SetWriteDeadline(time.Now().Add(WriteTimeout))
	return c.conn.WriteJSON(v)
}

func (c *Client) WriteControl(messageType int, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.isClosed {
		return errors.New("connection closed")
	}
	return c.conn.WriteControl(messageType, data, time.Now().Add(WriteTimeout))
}

// truncate 截断字符串用于日志显示
func truncate(s string, max int) string {
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// ProcessChat 处理单个聊天请求的完整流程
// It is designed to be called by only one goroutine at a time (guaranteed by Pool).
func (c *Client) ProcessChat(userMessage string, onChunk func(string), onDone func(string)) error {
	_, err := c.ProcessChatWithSession("", "", userMessage, onChunk, onDone)
	return err
}

func (c *Client) ProcessChatWithSession(dialogID, userID, userMessage string, onChunk func(string), onDone func(string)) (string, error) {
	// 1. 设置响应处理器
	responseCh := make(chan types.WSMessage, 100) // 增加缓冲，避免阻塞 readLoop

	c.mu.Lock()
	c.messageHandler = func(msg types.WSMessage) {
		select {
		case responseCh <- msg:
			return
		default:
		}

		if msg.Type == types.MsgTypeText {
			return
		}

		select {
		case <-responseCh:
		default:
		}

		select {
		case responseCh <- msg:
		default:
			logger.Warn("警告: 响应通道已满，丢弃消息", zap.String("type", msg.Type))
		}
	}
	c.mu.Unlock()

	// 退出时清理
	defer func() {
		c.mu.Lock()
		c.messageHandler = nil
		c.mu.Unlock()
	}()

	// 2. 启动会话 (Start Session)
	if dialogID == "" {
		dialogID = uuid.New().String()
	}
	if userID == "" {
		userID = "user_" + uuid.New().String()[:8]
	}

	logger.Info("开始新会话", zap.String("dialog_id", dialogID), zap.String("user_id", userID))

	startMsg := types.WSMessage{
		Type:        types.MsgTypeStart,
		DialogID:    dialogID,
		UserID:      userID,
		SendType:    "1", // 文字
		ReceiveType: "1", // 文字
	}

	logger.Info("发送上游消息", zap.String("type", startMsg.Type), zap.String("dialog_id", startMsg.DialogID), zap.String("user_id", startMsg.UserID))
	if err := c.WriteJSON(startMsg); err != nil {
		logger.Error("发送 Start 消息失败", zap.Error(err))
		return dialogID, err
	}

	// 3. 等待启动确认
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()

	started := false
	for !started {
		select {
		case msg := <-responseCh:
			if msg.Type == types.MsgTypeStart {
				if msg.DialogID != "" {
					dialogID = msg.DialogID
				}
				started = true
				logger.Info("会话启动成功", zap.String("dialog_id", dialogID))
			}
		case <-timeout.C:
			return dialogID, errors.New("等待会话启动超时")
		case <-c.closeCh:
			return dialogID, errors.New("握手期间连接关闭")
		}
	}

	// 4. 开始语音/输入 (Start Speech)
	logger.Info("发送上游消息", zap.String("type", types.MsgTypeStartSpeech), zap.String("dialog_id", dialogID))
	if err := c.WriteJSON(types.WSMessage{Type: types.MsgTypeStartSpeech}); err != nil {
		return dialogID, err
	}

	_ = c.WriteJSON(types.WSMessage{Type: types.MsgTypeHeartbeat})

	// 5. 发送消息 (Send Message)
	sendMsg := types.WSMessage{
		Type: types.MsgTypeSendSpeechText,
		Text: userMessage,
	}
	logger.Info("发送上游消息",
		zap.String("type", sendMsg.Type),
		zap.String("dialog_id", dialogID),
		zap.Int("text_len", len(userMessage)),
		zap.String("text_preview", truncate(userMessage, 50)),
	)
	if err := c.WriteJSON(sendMsg); err != nil {
		return dialogID, err
	}
	logger.Debug("已发送用户消息", zap.String("content", userMessage))

	// 6. 结束语音/输入 (Stop Speech)
	logger.Info("发送上游消息", zap.String("type", types.MsgTypeStopSpeech), zap.String("dialog_id", dialogID))
	if err := c.WriteJSON(types.WSMessage{Type: types.MsgTypeStopSpeech}); err != nil {
		return dialogID, err
	}

	// 7. 读取内容循环 (Read Loop)
	resetTimer(timeout, 60*time.Second)

	for {
		select {
		case msg := <-responseCh:
			resetTimer(timeout, 60*time.Second)

			if msg.DialogID != "" && msg.DialogID != dialogID {
				continue
			}

			switch msg.Type {
			case types.MsgTypeText:
				logger.Info("处理 Text 分片",
					zap.String("dialog_id", dialogID),
					zap.Int("content_len", len(msg.Content)),
					zap.String("content_preview", truncate(msg.Content, 50)),
				)
				if onChunk != nil {
					onChunk(msg.Content)
				}
			case types.MsgTypePlayOver:
				logger.Info("会话结束 (PlayOver)", zap.String("dialog_id", dialogID))
				if onDone != nil {
					onDone("stop")
				}
				return dialogID, nil
			case types.MsgTypeNoSpeech:
				logger.Warn("未检测到语音或识别失败", zap.String("dialog_id", dialogID))
				return dialogID, errors.New("未检测到语音或识别失败")
			default:
				logger.Info("忽略未处理消息类型", zap.String("type", msg.Type), zap.String("dialog_id", dialogID))
			}
		case <-timeout.C:
			logger.Error("等待响应超时", zap.String("dialog_id", dialogID))
			return dialogID, errors.New("等待响应超时")
		case <-c.closeCh:
			return dialogID, errors.New("生成期间连接关闭")
		}
	}
}
