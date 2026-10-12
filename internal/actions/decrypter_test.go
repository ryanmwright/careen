package actions

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDecrypterPromptsOnlyWhenWaiting(t *testing.T) {
	const want = "\a>>> Waiting for your security key (touch it if it's blinking): decrypting node1's host key"
	slow := func(_ context.Context, _, _ string) (string, error) {
		time.Sleep(60 * time.Millisecond) // blocked on a touch
		return "secret", nil
	}
	fast := func(_ context.Context, _, _ string) (string, error) { return "secret", nil } // touch cached, or no touch policy

	var prompt bytes.Buffer
	d := &Decrypter{Prompt: &prompt, PromptAfter: 10 * time.Millisecond, Extract: slow}
	if v, err := d.Decrypt(context.Background(), "node1's host key", "f", "k"); err != nil || v != "secret" {
		t.Fatalf("got %q, %v", v, err)
	}
	if !strings.Contains(prompt.String(), want) {
		t.Errorf("slow decryption: prompt = %q", prompt.String())
	}

	prompt.Reset()
	d = &Decrypter{Prompt: &prompt, PromptAfter: time.Second, Extract: fast}
	if v, err := d.Decrypt(context.Background(), "node1's host key", "f", "k"); err != nil || v != "secret" {
		t.Fatalf("got %q, %v", v, err)
	}
	if prompt.Len() != 0 {
		t.Errorf("fast decryption printed %q, want nothing", prompt.String())
	}
}

func TestDecrypterFailsFastWhenNotInteractive(t *testing.T) {
	var prompt bytes.Buffer
	calls := 0
	d := &Decrypter{Prompt: &prompt, PromptAfter: time.Millisecond, Extract: func(_ context.Context, file, key string) (string, error) {
		calls++
		time.Sleep(20 * time.Millisecond)
		return "", errors.New("gpg: card operation timed out")
	}}
	if _, err := d.Decrypt(context.Background(), "node1's host key", "f", "k"); err == nil {
		t.Fatal("want the error")
	}
	if calls != 1 {
		t.Errorf("non-interactive tried %d times, want 1", calls)
	}
	if !strings.Contains(prompt.String(), ">>> Waiting for your security key") {
		t.Errorf("prompt = %q", prompt.String())
	}

	d = &Decrypter{Extract: func(_ context.Context, file, key string) (string, error) { return "secret", nil }}
	if v, err := d.Decrypt(context.Background(), "x", "f", "k"); err != nil || v != "secret" {
		t.Fatalf("got %q, %v", v, err)
	}
}
