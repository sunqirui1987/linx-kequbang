package upstream

import (
	"sync"
	"time"

	"linx-kequbang/pkg/logger"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Manager 管理基于 API Token 的连接池
// Manager handles a pool of connections keyed by API token.
type Manager struct {
	pools    sync.Map // map[string]*TokenPool
	sessions sync.Map // map[string]*Session
}

// TokenPool 针对单个 Token 的连接池
type TokenPool struct {
	token    string
	clients  chan *Client
	mu       sync.Mutex
	maxSize  int
	currSize int
}

type Session struct {
	DialogID  string
	UserID    string
	UpdatedAt time.Time
	CreatedAt time.Time
}

var globalManager *Manager
var once sync.Once

// GetManager 返回单例 Manager 实例
// GetManager returns the singleton manager instance.
func GetManager() *Manager {
	once.Do(func() {
		globalManager = &Manager{}
	})
	return globalManager
}

func (m *Manager) sessionKey(token, convKey string) string {
	return token + "|" + convKey
}

func (m *Manager) GetOrCreateSession(token, convKey string) Session {
	key := m.sessionKey(token, convKey)
	if v, ok := m.sessions.Load(key); ok {
		return v.(Session)
	}

	now := time.Now()
	sess := Session{
		DialogID:  uuid.NewSHA1(uuid.NameSpaceOID, []byte("dialog|"+token+"|"+convKey)).String(),
		UserID:    "user_" + uuid.NewSHA1(uuid.NameSpaceOID, []byte("user|"+token)).String()[:8],
		UpdatedAt: now,
		CreatedAt: now,
	}
	m.sessions.Store(key, sess)
	return sess
}

func (m *Manager) UpdateSession(token, convKey, dialogID, userID string) {
	key := m.sessionKey(token, convKey)
	now := time.Now()

	sess := Session{
		DialogID:  dialogID,
		UserID:    userID,
		UpdatedAt: now,
		CreatedAt: now,
	}
	if v, ok := m.sessions.Load(key); ok {
		old := v.(Session)
		sess.CreatedAt = old.CreatedAt
	}
	m.sessions.Store(key, sess)
}

// Acquire 获取一个可用连接
// Acquire gets a client for the given token.
func (m *Manager) Acquire(token string) (*Client, error) {
	poolI, _ := m.pools.LoadOrStore(token, &TokenPool{
		token:   token,
		clients: make(chan *Client, 50), // 每个 Token 最多 50 个空闲连接
		maxSize: 100,                    // 软限制 (逻辑上暂未严格执行)
	})
	pool := poolI.(*TokenPool)

	// 尝试获取空闲连接
	select {
	case client := <-pool.clients:
		if client.IsAlive() {
			logger.Debug("重用现有连接", zap.String("token_prefix", tokenPreview(token)))
			return client, nil
		}
		// 如果连接已死，创建新连接
		logger.Debug("连接池中发现无效连接，丢弃")
	default:
		// 无空闲连接，创建新连接
	}

	logger.Info("创建新连接", zap.String("token_prefix", tokenPreview(token)))
	return NewClient(token)
}

// Release 将连接归还到池中
// Release returns a client to the pool.
func (m *Manager) Release(client *Client) {
	if !client.IsAlive() {
		logger.Debug("连接已关闭，不归还到池中")
		return
	}

	poolI, ok := m.pools.Load(client.token)
	if !ok {
		client.Close()
		return
	}
	pool := poolI.(*TokenPool)

	// 尝试放回池中
	select {
	case pool.clients <- client:
		// 成功归还
		logger.Debug("连接已归还到池中")
	default:
		// 池已满，关闭连接
		logger.Warn("连接池已满，关闭多余连接", zap.String("token_prefix", tokenPreview(client.token)))
		client.Close()
	}
}
