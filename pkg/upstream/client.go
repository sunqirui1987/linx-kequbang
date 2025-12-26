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
	HeartbeatPeriod = 30 * time.Second
	ReadTimeout     = 60 * time.Second
	WriteTimeout    = 10 * time.Second
)

// Client 封装了与上游服务的 WebSocket 连接
// Client wraps a websocket connection to the upstream service.
type Client struct {
	conn           *websocket.Conn
	token          string
	mu             sync.Mutex // 保护写操作 Protects write operations
	readCh         chan types.WSMessage
	errorCh        chan error
	closeCh        chan struct{}
	isClosed       bool
	lastUsed       time.Time
	messageHandler func(types.WSMessage) // 当前活跃请求的回调 Callback for current active request
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

	c := &Client{
		conn:     conn,
		token:    token,
		readCh:   make(chan types.WSMessage, 100),
		errorCh:  make(chan error, 1),
		closeCh:  make(chan struct{}),
		lastUsed: time.Now(),
	}

	logger.Info("建立了新的 WebSocket 连接", zap.String("token_prefix", token[:10]+"..."))

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
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure) && !c.isClosed {
				logger.Error("WS 读取错误", zap.Error(err))
			}
			return
		}

		// 处理心跳响应
		if msg.Type == types.MsgTypeHeartbeat {
			logger.Debug("收到心跳响应")
			continue
		}

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
	c.lastUsed = time.Now()

	// 1. 设置响应处理器
	responseCh := make(chan types.WSMessage, 100) // 增加缓冲，避免阻塞 readLoop

	c.mu.Lock()
	c.messageHandler = func(msg types.WSMessage) {
		select {
		case responseCh <- msg:
		default:
			// 如果缓冲满了，丢弃消息并记录警告
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
	// 每次请求生成新的 DialogID 以保证无状态
	dialogID := uuid.New().String()
	userID := "user_" + uuid.New().String()[:8]

	logger.Info("开始新会话", zap.String("dialog_id", dialogID), zap.String("user_id", userID))

	startMsg := types.WSMessage{
		Type:        types.MsgTypeStart,
		DialogID:    dialogID,
		UserID:      userID,
		SendType:    "1", // 文字
		ReceiveType: "1", // 文字
	}

	if err := c.WriteJSON(startMsg); err != nil {
		logger.Error("发送 Start 消息失败", zap.Error(err))
		return err
	}

	// 3. 等待启动确认
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()

	started := false
	for !started {
		select {
		case msg := <-responseCh:
			if msg.Type == types.MsgTypeStart {
				started = true
				logger.Info("会话启动成功", zap.String("dialog_id", dialogID))
			}
		case <-timeout.C:
			return errors.New("等待会话启动超时")
		case <-c.closeCh:
			return errors.New("握手期间连接关闭")
		}
	}

	// 4. 开始语音/输入 (Start Speech)
	if err := c.WriteJSON(types.WSMessage{Type: types.MsgTypeStartSpeech}); err != nil {
		return err
	}

	// 5. 发送消息 (Send Message)
	sendMsg := types.WSMessage{
		Type: types.MsgTypeSendSpeechText,
		Text: userMessage,
	}
	if err := c.WriteJSON(sendMsg); err != nil {
		return err
	}
	logger.Debug("已发送用户消息", zap.String("content", userMessage))

	// 6. 结束语音/输入 (Stop Speech)
	if err := c.WriteJSON(types.WSMessage{Type: types.MsgTypeStopSpeech}); err != nil {
		return err
	}

	// 7. 读取内容循环 (Read Loop)
	timeout.Reset(60 * time.Second)

	for {
		select {
		case msg := <-responseCh:
			timeout.Reset(60 * time.Second) // 收到消息重置超时

			switch msg.Type {
			case types.MsgTypeText:
				logger.Debug("收到 Text 消息", zap.String("content_preview", truncate(msg.Content, 10)))
				if onChunk != nil {
					onChunk(msg.Content)
				}
			case types.MsgTypePlayOver:
				logger.Info("会话结束 (PlayOver)", zap.String("dialog_id", dialogID))
				if onDone != nil {
					onDone("stop")
				}
				return nil
			case types.MsgTypeNoSpeech:
				logger.Warn("未检测到语音或识别失败", zap.String("dialog_id", dialogID))
				return errors.New("未检测到语音或识别失败")
			}
		case <-timeout.C:
			logger.Error("等待响应超时", zap.String("dialog_id", dialogID))
			return errors.New("等待响应超时")
		case <-c.closeCh:
			return errors.New("生成期间连接关闭")
		}
	}
}
