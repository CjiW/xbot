package channel

import (
	"xbot/storage/sqlite"
	"xbot/tools"
)

// RunnerCallbacks groups runner management closures shared between Web and Feishu channels.
type RunnerCallbacks struct {
	RunnerTokenGet      func(senderID string) string
	RunnerTokenGenerate func(senderID, mode, dockerImage, workspace string) (string, error)
	RunnerTokenRevoke   func(senderID string) error
	RunnerList          func(senderID string) ([]tools.RunnerInfo, error)
	RunnerCreate        func(senderID, name, mode, dockerImage, workspace string, llm tools.RunnerLLMSettings) (string, error)
	RunnerDelete        func(senderID, name string) error
	RunnerGetActive     func(senderID string) (string, error)
	RunnerSetActive     func(senderID, name string) error
}

// RegistryCallbacks groups registry management closures shared between Web and Feishu channels.
type RegistryCallbacks struct {
	RegistryBrowse    func(entryType string, limit, offset int) ([]sqlite.SharedEntry, error)
	RegistryInstall   func(entryType string, id int64, senderID string) error
	RegistryListMy    func(senderID, entryType string) ([]sqlite.SharedEntry, []string, error)
	RegistryPublish   func(entryType, name, senderID string) error
	RegistryUnpublish func(entryType, name, senderID string) error
	RegistryUninstall func(entryType, name, senderID string) error
}

// LLMCallbacks groups LLM management closures shared between Web and Feishu channels.
type LLMCallbacks struct {
	LLMList                   func(senderID string) ([]string, string)
	LLMSet                    func(senderID, model string) error
	LLMGetMaxContext          func(senderID string) int
	LLMSetMaxContext          func(senderID string, maxContext int) error
	LLMGetMaxOutputTokens     func(senderID string) int
	LLMSetMaxOutputTokens     func(senderID string, maxTokens int) error
	LLMGetThinkingMode        func(senderID string) string
	LLMSetThinkingMode        func(senderID string, mode string) error
	LLMGetPersonalConcurrency func(senderID string) int
	LLMSetPersonalConcurrency func(senderID string, personal int) error
}
