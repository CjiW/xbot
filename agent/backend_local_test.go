package agent

import (
	"testing"

	"xbot/config"
	"xbot/llm"
	"xbot/storage/sqlite"
)

func TestLocalBackendSetDefaultSubscriptionRefreshesFactoryCache(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	db, err := sqlite.Open(config.DBFilePath())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	defaultLLM := &llm.MockLLM{}
	factory := NewLLMFactory(sqlite.NewUserLLMConfigService(db), defaultLLM, "default-model")
	subSvc := sqlite.NewLLMSubscriptionService(db)
	factory.SetSubscriptionSvc(subSvc)
	if err := subSvc.Add(&sqlite.LLMSubscription{ID: "sub-a", SenderID: "cli_user", Name: "a", Provider: "openai", BaseURL: "https://a.example/v1", APIKey: "sk-a", Model: "model-a", IsDefault: true}); err != nil {
		t.Fatalf("add a: %v", err)
	}
	if err := subSvc.Add(&sqlite.LLMSubscription{ID: "sub-b", SenderID: "cli_user", Name: "b", Provider: "openai", BaseURL: "https://b.example/v1", APIKey: "sk-b", Model: "model-b", IsDefault: false}); err != nil {
		t.Fatalf("add b: %v", err)
	}

	a := &Agent{llmFactory: factory}
	backend := &LocalBackend{agent: a}

	_, model, _, _ := factory.GetLLM("cli_user")
	if model != "model-a" {
		t.Fatalf("expected initial model-a, got %q", model)
	}
	if err := backend.SetDefaultSubscription("sub-b"); err != nil {
		t.Fatalf("SetDefaultSubscription: %v", err)
	}
	_, model, _, _ = factory.GetLLM("cli_user")
	if model != "model-b" {
		t.Fatalf("expected switched model-b, got %q", model)
	}
}
