package sqlite

import (
	"database/sql"
	"fmt"
	"time"

	"xbot/crypto"
	log "xbot/logger"
)

// UserLLMConfig 用户 LLM 配置
type UserLLMConfig struct {
	SenderID     string    // 用户 ID
	Provider     string    // LLM 提供商: "openai", "deepseek", "anthropic" 等
	BaseURL      string    // API Base URL
	APIKey       string    // API Key
	Model        string    // 默认模型
	MaxContext   int       // 最大上下文 token 数（0 表示不限制）
	ThinkingMode string    // 思考模式: "" (自动), "enabled", "disabled"
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

	// Try user_llm_subscriptions first (new table)
	var cfg UserLLMConfig
	var createdAt, updatedAt sql.NullTime
	err := conn.QueryRow(`
		SELECT sender_id, provider, base_url, api_key, model, created_at, updated_at
		FROM user_llm_subscriptions
		WHERE sender_id = ? AND is_default = 1
		LIMIT 1
	`, senderID).Scan(
		&cfg.SenderID, &cfg.Provider, &cfg.BaseURL, &cfg.APIKey, &cfg.Model,
		&createdAt, &updatedAt,
	)

	if err == sql.ErrNoRows {
		// Fallback to user_llm_configs (old table)
		var thinkingMode sql.NullString
		err = conn.QueryRow(`
			SELECT sender_id, provider, base_url, api_key, model, max_context, thinking_mode, created_at, updated_at
			FROM user_llm_configs
			WHERE sender_id = ?
		`, senderID).Scan(
			&cfg.SenderID, &cfg.Provider, &cfg.BaseURL, &cfg.APIKey, &cfg.Model, &cfg.MaxContext,
			&thinkingMode, &createdAt, &updatedAt,
		)
		if err == sql.ErrNoRows {
			return nil, nil // 无配置
		}
		if err != nil {
			return nil, fmt.Errorf("query user llm config (fallback): %w", err)
		}
		if thinkingMode.Valid {
			cfg.ThinkingMode = thinkingMode.String
		}
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

	// Decrypt API key
	if cfg.APIKey != "" {
		decrypted, err := crypto.Decrypt(cfg.APIKey)
		if err != nil {
			log.WithError(err).WithField("sender_id", cfg.SenderID).Error("failed to decrypt API key")
			return nil, fmt.Errorf("decrypt API key: %w", err)
		}
		cfg.APIKey = decrypted
	}

	return &cfg, nil
}

// SetConfig 设置用户的 LLM 配置（写入 user_llm_subscriptions + user_llm_configs）
func (s *UserLLMConfigService) SetConfig(cfg *UserLLMConfig) error {
	conn := s.db.Conn()

	// Encrypt API key before storage
	encryptedAPIKey := cfg.APIKey
	if cfg.APIKey != "" {
		encrypted, err := crypto.Encrypt(cfg.APIKey)
		if err != nil {
			log.WithError(err).WithField("sender_id", cfg.SenderID).Error("failed to encrypt API key")
			return fmt.Errorf("encrypt API key: %w", err)
		}
		encryptedAPIKey = encrypted
	}

	now := time.Now()

	// --- Write to user_llm_configs (old table, backward compat) ---
	_, err := conn.Exec(`
		INSERT INTO user_llm_configs (sender_id, provider, base_url, api_key, model, max_context, thinking_mode, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(sender_id) DO UPDATE SET
			provider = excluded.provider,
			base_url = excluded.base_url,
			api_key = excluded.api_key,
			model = excluded.model,
			max_context = excluded.max_context,
			thinking_mode = excluded.thinking_mode,
			updated_at = excluded.updated_at
	`, cfg.SenderID, cfg.Provider, cfg.BaseURL, encryptedAPIKey, cfg.Model, cfg.MaxContext, cfg.ThinkingMode, now, now)
	if err != nil {
		return fmt.Errorf("upsert user_llm_configs: %w", err)
	}

	// --- Write to user_llm_subscriptions (new table) ---
	name := cfg.Provider
	if name == "" {
		name = "openai"
	}

	tx, err := conn.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	tx.Exec("UPDATE user_llm_subscriptions SET is_default = 0 WHERE sender_id = ?", cfg.SenderID)

	result, err := tx.Exec(`
		UPDATE user_llm_subscriptions SET
			name = ?, provider = ?, base_url = ?, api_key = ?, model = ?,
			is_default = 1, updated_at = ?
		WHERE sender_id = ? AND provider = ?
	`, name, cfg.Provider, cfg.BaseURL, encryptedAPIKey, cfg.Model, now, cfg.SenderID, cfg.Provider)
	if err != nil {
		return fmt.Errorf("update subscription: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		subID := fmt.Sprintf("sub_%x", now.UnixNano())
		_, err = tx.Exec(`
			INSERT INTO user_llm_subscriptions (id, sender_id, name, provider, base_url, api_key, model, is_default, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
		`, subID, cfg.SenderID, name, cfg.Provider, cfg.BaseURL, encryptedAPIKey, cfg.Model, now, now)
		if err != nil {
			return fmt.Errorf("insert subscription: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}

	log.WithFields(log.Fields{
		"sender_id": cfg.SenderID,
		"provider":  cfg.Provider,
		"model":     cfg.Model,
	}).Info("User LLM config saved")

	return nil
}

// DeleteConfig 删除用户的 LLM 配置（两张表都删）
func (s *UserLLMConfigService) DeleteConfig(senderID string) error {
	conn := s.db.Conn()
	if _, err := conn.Exec("DELETE FROM user_llm_configs WHERE sender_id = ?", senderID); err != nil {
		return fmt.Errorf("delete user_llm_configs: %w", err)
	}
	if _, err := conn.Exec("DELETE FROM user_llm_subscriptions WHERE sender_id = ?", senderID); err != nil {
		return fmt.Errorf("delete user_llm_subscriptions: %w", err)
	}
	log.WithField("sender_id", senderID).Info("User LLM config deleted")
	return nil
}
