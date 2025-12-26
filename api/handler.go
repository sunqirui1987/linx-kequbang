package api

import (
	"encoding/json"
	"fmt"
	"io"
	"linx-kequbang/pkg/logger"
	"linx-kequbang/pkg/upstream"
	"linx-kequbang/types"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// ChatCompletions 处理 OpenAI 格式的聊天补全请求
func ChatCompletions(c *gin.Context) {
	var req types.ChatCompletionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		logger.Warn("请求参数解析失败", zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	authHeader := c.GetHeader("Authorization")
	if authHeader == "" {
		logger.Warn("请求缺少 Authorization 头")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Missing Authorization header"})
		return
	}

	// 提取用户消息 (Extract user message)
	var firstUserMessage string
	var userMessage string
	for _, msg := range req.Messages {
		if msg.Role == "user" {
			if firstUserMessage == "" {
				firstUserMessage = msg.Content
			}
			userMessage = msg.Content
		}
	}
	if userMessage == "" {
		logger.Warn("未找到用户消息内容")
		c.JSON(http.StatusBadRequest, gin.H{"error": "No user message found"})
		return
	}

	convKey := uuid.NewSHA1(uuid.NameSpaceOID, []byte(firstUserMessage)).String()

	logger.Info("收到聊天请求",
		zap.String("model", req.Model),
		zap.Bool("stream", req.Stream),
		zap.String("user_msg_preview", truncate(userMessage, 20)),
	)

	// 从连接池获取客户端 (Get client from pool)
	mgr := upstream.GetManager()
	client, err := mgr.Acquire(authHeader)
	if err != nil {
		logger.Error("获取上游连接失败", zap.Error(err))
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Failed to acquire upstream connection: " + err.Error()})
		return
	}
	defer mgr.Release(client)

	sess := mgr.GetOrCreateSession(authHeader, convKey)

	// 流式 vs 非流式 (Streaming vs Non-Streaming)
	if req.Stream {
		handleStream(c, mgr, authHeader, convKey, sess, client, req.Model, userMessage)
	} else {
		handleNormal(c, mgr, authHeader, convKey, sess, client, req.Model, userMessage)
	}
}

// truncate 截断字符串用于日志显示
func truncate(s string, max int) string {
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// handleNormal 处理非流式响应
func handleNormal(c *gin.Context, mgr *upstream.Manager, token, convKey string, sess upstream.Session, client *upstream.Client, model, userMessage string) {
	var fullContent strings.Builder

	dialogID, err := client.ProcessChatWithSession(sess.DialogID, sess.UserID, userMessage, func(chunk string) {
		fullContent.WriteString(chunk)
	}, nil)

	if err != nil {
		logger.Error("上游处理失败 (非流式)", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Upstream error: " + err.Error()})
		return
	}

	mgr.UpdateSession(token, convKey, dialogID, sess.UserID)

	content := fullContent.String()
	resp := types.ChatCompletionResponse{
		ID:      "chatcmpl-" + uuid.New().String(),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []types.ChatCompletionChoice{
			{
				Index: 0,
				Message: types.ChatCompletionMessage{
					Role:    "assistant",
					Content: content,
				},
				FinishReason: "stop",
			},
		},
		Usage: types.Usage{
			PromptTokens:     len(userMessage), // 简单估算
			CompletionTokens: len(content),     // 简单估算
			TotalTokens:      len(userMessage) + len(content),
		},
	}
	c.JSON(http.StatusOK, resp)
}

// handleStream 处理流式响应 (SSE)
func handleStream(c *gin.Context, mgr *upstream.Manager, token, convKey string, sess upstream.Session, client *upstream.Client, model, userMessage string) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	id := "chatcmpl-" + uuid.New().String()
	created := time.Now().Unix()

	// 检查是否支持 Flush
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		logger.Warn("ResponseWriter 不支持 Flush，流式响应可能无法实时推送")
	}

	c.Stream(func(w io.Writer) bool {
		// 1. Send Initial Chunk (Role: assistant)
		firstResp := types.ChatCompletionStreamResponse{
			ID:      id,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   model,
			Choices: []types.ChatCompletionStreamChoice{
				{
					Index: 0,
					Delta: types.ChatCompletionMessage{
						Role: "assistant",
					},
					FinishReason: nil,
				},
			},
		}
		writeSSE(w, firstResp)
		if ok {
			flusher.Flush()
		}

		// 2. Process chunks
		dialogID, err := client.ProcessChatWithSession(sess.DialogID, sess.UserID, userMessage, func(chunk string) {
			resp := types.ChatCompletionStreamResponse{
				ID:      id,
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   model,
				Choices: []types.ChatCompletionStreamChoice{
					{
						Index: 0,
						Delta: types.ChatCompletionMessage{
							Content: chunk,
						},
						FinishReason: nil,
					},
				},
			}
			writeSSE(w, resp)
			if ok {
				flusher.Flush()
			}
		}, func(reason string) {
			// 3. Send Final Chunk (FinishReason: stop)
			resp := types.ChatCompletionStreamResponse{
				ID:      id,
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   model,
				Choices: []types.ChatCompletionStreamChoice{
					{
						Index:        0,
						Delta:        types.ChatCompletionMessage{}, // Empty delta
						FinishReason: "stop",
					},
				},
			}
			writeSSE(w, resp)
			fmt.Fprintf(w, "data: [DONE]\n\n")
			if ok {
				flusher.Flush()
			}
		})

		if err != nil {
			logger.Error("上游处理中断 (流式)", zap.Error(err))
			return false
		}
		mgr.UpdateSession(token, convKey, dialogID, sess.UserID)
		logger.Info("流式请求处理完成")
		return false
	})
}

// writeSSE writes a data-only SSE message (without event: message)
func writeSSE(w io.Writer, v interface{}) {
	data, err := json.Marshal(v)
	if err != nil {
		logger.Error("JSON marshal failed", zap.Error(err))
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", data)
}
