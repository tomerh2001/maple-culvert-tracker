package data

import "testing"

func TestIsBotOwner(t *testing.T) {
	t.Setenv(EnvVarBotOwnerUserID, "")
	if IsBotOwner("") || IsBotOwner("123") {
		t.Error("unset env must never match")
	}
	t.Setenv(EnvVarBotOwnerUserID, " 123 ")
	if !IsBotOwner("123") {
		t.Error("configured id must match (trimmed)")
	}
	if IsBotOwner("456") || IsBotOwner("") {
		t.Error("other ids must not match")
	}
}
