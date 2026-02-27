package sqlite

import (
	"testing"
)

func TestTenantService_GetOrCreateTenantID(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	svc := NewTenantService(db)

	// Create first tenant
	id1, err := svc.GetOrCreateTenantID("feishu", "chat123")
	if err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}
	if id1 == 0 {
		t.Error("Expected non-zero tenant ID")
	}

	// Get same tenant - should return same ID
	id2, err := svc.GetOrCreateTenantID("feishu", "chat123")
	if err != nil {
		t.Fatalf("Failed to get tenant: %v", err)
	}
	if id2 != id1 {
		t.Errorf("Expected same tenant ID %d, got %d", id1, id2)
	}

	// Create different tenant - should return different ID
	id3, err := svc.GetOrCreateTenantID("feishu", "chat456")
	if err != nil {
		t.Fatalf("Failed to create second tenant: %v", err)
	}
	if id3 == id1 {
		t.Error("Expected different tenant ID for different chat")
	}

	// Create tenant with different channel
	id4, err := svc.GetOrCreateTenantID("slack", "chat123")
	if err != nil {
		t.Fatalf("Failed to create tenant with different channel: %v", err)
	}
	if id4 == id1 || id4 == id3 {
		t.Error("Expected different tenant ID for different channel")
	}
}

func TestTenantService_GetTenantInfo(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	svc := NewTenantService(db)

	// Create tenant
	tenantID, err := svc.GetOrCreateTenantID("feishu", "test_chat")
	if err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}

	// Get tenant info
	channel, chatID, err := svc.GetTenantInfo(tenantID)
	if err != nil {
		t.Fatalf("Failed to get tenant info: %v", err)
	}

	if channel != "feishu" {
		t.Errorf("Expected channel 'feishu', got '%s'", channel)
	}
	if chatID != "test_chat" {
		t.Errorf("Expected chatID 'test_chat', got '%s'", chatID)
	}

	// Try to get non-existent tenant
	_, _, err = svc.GetTenantInfo(99999)
	if err == nil {
		t.Error("Expected error for non-existent tenant")
	}
}

func TestTenantService_DeleteTenant(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	svc := NewTenantService(db)

	// Create tenant
	tenantID, err := svc.GetOrCreateTenantID("feishu", "to_delete")
	if err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}

	// Delete tenant
	err = svc.DeleteTenant(tenantID)
	if err != nil {
		t.Fatalf("Failed to delete tenant: %v", err)
	}

	// Try to get deleted tenant
	_, _, err = svc.GetTenantInfo(tenantID)
	if err == nil {
		t.Error("Expected error for deleted tenant")
	}

	// Try to delete non-existent tenant
	err = svc.DeleteTenant(99999)
	if err == nil {
		t.Error("Expected error when deleting non-existent tenant")
	}
}

func TestTenantService_ListTenants(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	svc := NewTenantService(db)

	// Create multiple tenants
	ids := []int64{}
	for i := 0; i < 3; i++ {
		id, err := svc.GetOrCreateTenantID("feishu", "chat"+string(rune('0'+i)))
		if err != nil {
			t.Fatalf("Failed to create tenant: %v", err)
		}
		ids = append(ids, id)
	}

	// List tenants
	tenants, err := svc.ListTenants()
	if err != nil {
		t.Fatalf("Failed to list tenants: %v", err)
	}

	if len(tenants) != 3 {
		t.Errorf("Expected 3 tenants, got %d", len(tenants))
	}

	// Verify tenant IDs
	idMap := make(map[int64]bool)
	for _, tenant := range tenants {
		idMap[tenant.ID] = true
		if tenant.Channel != "feishu" {
			t.Errorf("Expected channel 'feishu', got '%s'", tenant.Channel)
		}
	}
	for _, id := range ids {
		if !idMap[id] {
			t.Errorf("Tenant ID %d not found in list", id)
		}
	}
}
