package upstream

import (
	"sync"

	"linx-kequbang/pkg/logger"

	"go.uber.org/zap"
)

// Manager 管理基于 API Token 的连接池
// Manager handles a pool of connections keyed by API token.
type Manager struct {
	pools sync.Map // map[string]*TokenPool
}

// TokenPool 针对单个 Token 的连接池
type TokenPool struct {
	token    string
	clients  chan *Client
	mu       sync.Mutex
	maxSize  int
	currSize int
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
			logger.Debug("重用现有连接", zap.String("token_prefix", token[:10]+"..."))
			return client, nil
		}
		// 如果连接已死，创建新连接
		logger.Debug("连接池中发现无效连接，丢弃")
	default:
		// 无空闲连接，创建新连接
	}

	logger.Info("创建新连接", zap.String("token_prefix", token[:10]+"..."))
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
		logger.Warn("连接池已满，关闭多余连接", zap.String("token_prefix", client.token[:10]+"..."))
		client.Close()
	}
}
