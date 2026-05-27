package domain

import (
	"strings"
	"testing"
)

func TestValidateCommentBody_WithinLimit(t *testing.T) {
	// 8000 ASCII runes → should pass.
	body := strings.Repeat("x", 8000)
	if err := ValidateCommentBody(body); err != nil {
		t.Fatalf("ValidateCommentBody(8000 runes) = %v, want nil", err)
	}
}

func TestValidateCommentBody_Exactly8000Runes(t *testing.T) {
	// Exactly at limit.
	body := strings.Repeat("a", MaxCommentBodyRunes)
	if err := ValidateCommentBody(body); err != nil {
		t.Fatalf("ValidateCommentBody(exactly %d runes) = %v, want nil", MaxCommentBodyRunes, err)
	}
}

func TestValidateCommentBody_ExceedsLimit(t *testing.T) {
	// 8001 ASCII runes → should fail.
	body := strings.Repeat("b", 8001)
	err := ValidateCommentBody(body)
	if err == nil {
		t.Fatal("ValidateCommentBody(8001 runes) = nil, want error")
	}
	tooLong, ok := err.(*ErrCommentBodyTooLong)
	if !ok {
		t.Fatalf("ValidateCommentBody returned %T, want *ErrCommentBodyTooLong", err)
	}
	if tooLong.Runes != 8001 {
		t.Errorf("Runes = %d, want 8001", tooLong.Runes)
	}
	if tooLong.Limit != MaxCommentBodyRunes {
		t.Errorf("Limit = %d, want %d", tooLong.Limit, MaxCommentBodyRunes)
	}
}

func TestValidateCommentBody_ChineseCharacters_UnderLimit(t *testing.T) {
	// 8000 Chinese characters (each 3 bytes in UTF-8, 24000 bytes total).
	// Should pass because rune count = 8000 ≤ MaxCommentBodyRunes.
	body := strings.Repeat("中", 8000)
	if err := ValidateCommentBody(body); err != nil {
		t.Fatalf("ValidateCommentBody(8000 中) = %v, want nil", err)
	}
}

func TestValidateCommentBody_ChineseCharacters_ExceedsLimit(t *testing.T) {
	// 8001 Chinese characters (24003 bytes) → should fail with rune count 8001.
	body := strings.Repeat("中", 8001)
	err := ValidateCommentBody(body)
	if err == nil {
		t.Fatal("ValidateCommentBody(8001 中) = nil, want error")
	}
	tooLong, ok := err.(*ErrCommentBodyTooLong)
	if !ok {
		t.Fatalf("ValidateCommentBody returned %T, want *ErrCommentBodyTooLong", err)
	}
	if tooLong.Runes != 8001 {
		t.Errorf("Runes = %d, want 8001", tooLong.Runes)
	}
}

func TestValidateCommentBody_Empty(t *testing.T) {
	// Empty body is NOT the domain's responsibility — it should pass.
	if err := ValidateCommentBody(""); err != nil {
		t.Fatalf("ValidateCommentBody(\"\") = %v, want nil", err)
	}
}

func TestErrCommentBodyTooLong_Error(t *testing.T) {
	e := &ErrCommentBodyTooLong{Runes: 8517, Limit: 8000}
	got := e.Error()
	want := "comment body too long: 8517 runes (limit 8000)"
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}
