package sqlite

import (
	"database/sql"
	"fmt"
	"time"

	log "xbot/logger"
)

// UserLLMConfig 用户 LLM 配置
type UserLLMConfig struct {
	SenderID     string    // 用户 ID
	Provider     string    // LLM 提供商: "openai", "codebuddy", "deepseek" 等
	BaseURL      string    // API Base URL
	APIKey       string    // API Key
	Model        string    // 默认模型
	UserID       string    // CodeBuddy 专用: X-User-Id
	EnterpriseID string    // CodeBuddy 专用: X-Enterprise-Id
	Domain       string    // CodeBuddy 专用: X-Domain
	CreatedAt    time.Time // 创建时间
	UpdatedAt    time.Time // 更新时间
}

// UserLLMConfigService 用户 LLM 配置服务
type UserLLMConfigService struct {
	db *DB
}

// NewUserLLMConfigService 创建用户 LLM 配置服务
func NewUserLLMConfigService(db *DB) *UserLLMConfigService {
	return &UserLLMConfigService{db: db}
}

// GetConfig 获取用户的 LLM 配置
func (s *UserLLMConfigService) GetConfig(senderID string) (*UserLLMConfig, error) {
	conn := s.db.Conn()

	var cfg UserLLMConfig
	var createdAt, updatedAt sql.NullTime
	err := conn.QueryRow(`
		SELECT sender_id, provider, base_url, api_key, model, user_id, enterprise_id, domain, created_at, updated_at
		FROM user_llm_configs
		WHERE sender_id = ?
	`, senderID).Scan(
		&cfg.SenderID, &cfg.Provider, &cfg.BaseURL, &cfg.APIKey, &cfg.Model,
		&cfg.UserID, &cfg.EnterpriseID, &cfg.Domain,
		&createdAt, &updatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, nil // 无配置
	}
	if err != nil {
		return nil, fmt.Errorf("query user llm config: %w", err)
	}

	if createdAt.Valid {
		cfg.CreatedAt = createdAt.Time
	}
	if updatedAt.Valid {
		cfg.UpdatedAt = updatedAt.Time
	}

	return &cfg, nil
}

// SetConfig 设置用户的 LLM 配置
func (s *UserLLMConfigService) SetConfig(cfg *UserLLMConfig) error {
	conn := s.db.Conn()

	now := time.Now()
	_, err := conn.Exec(`
		INSERT INTO user_llm_configs (sender_id, provider, base_url, api_key, model, user_id, enterprise_id, domain, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(sender_id) DO UPDATE SET
			provider = excluded.provider,
			base_url = excluded.base_url,
			api_key = excluded.api_key,
			model = excluded.model,
			user_id = excluded.user_id,
			enterprise_id = excluded.enterprise_id,
			domain = excluded.domain,
			updated_at = excluded.updated_at
	`, cfg.SenderID, cfg.Provider, cfg.BaseURL, cfg.APIKey, cfg.Model,
		cfg.UserID, cfg.EnterpriseID, cfg.Domain, now, now,
	)

	if err != nil {
		return fmt.Errorf("upsert user llm config: %w", err)
	}

	log.WithFields(log.Fields{
		"sender_id": cfg.SenderID,
		"provider":  cfg.Provider,
		"model":     cfg.Model,
	}).Info("User LLM config saved")

	return nil
}

// DeleteConfig 删除用户的 LLM 配置
func (s *UserLLMConfigService) DeleteConfig(senderID string) error {
	conn := s.db.Conn()
	_, err := conn.Exec("DELETE FROM user_llm_configs WHERE sender_id = ?", senderID)
	if err != nil {
		return fmt.Errorf("delete user llm config: %w", err)
	}
	log.WithField("sender_id", senderID).Info("User LLM config deleted")
	return nil
}
