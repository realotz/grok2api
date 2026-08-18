package web

import (
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"testing"
	"time"
)

func TestGenerateLocalStatsigIsSelfConsistent(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC).Unix()
	value, err := generateLocalStatsig("post", "/rest/app-chat/conversations/new", now)
	if err != nil {
		t.Fatal(err)
	}
	if !validStatsigID(value) {
		t.Fatalf("generated id failed shape check: %q", value)
	}
	raw, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil || len(raw) != 70 {
		t.Fatalf("decode = %d %v", len(raw), err)
	}
	key := raw[0]
	seed, hex, err := currentStatsigPair()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 48; i++ {
		if raw[1+i]^key != seed[i] {
			t.Fatalf("seed byte %d mismatch", i)
		}
	}
	number := uint32(raw[49]^key) | uint32(raw[50]^key)<<8 | uint32(raw[51]^key)<<16 | uint32(raw[52]^key)<<24
	if number != uint32(now-statsigEpoch) {
		t.Fatalf("number = %d", number)
	}
	input := "POST!/rest/app-chat/conversations/new!" + strconv.FormatUint(uint64(number), 10) + statsigSalt + hex
	sum := sha256.Sum256([]byte(input))
	for i := 0; i < 16; i++ {
		if raw[53+i]^key != sum[i] {
			t.Fatalf("sha byte %d mismatch", i)
		}
	}
	if raw[69]^key != statsigMark {
		t.Fatalf("mark = %d", raw[69]^key)
	}
}

func TestGenerateLocalStatsigUsesFreshKey(t *testing.T) {
	now := time.Now().Unix()
	first, err := generateLocalStatsig(httpMethodPost, "/rest/app-chat/conversations/new", now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := generateLocalStatsig(httpMethodPost, "/rest/app-chat/conversations/new", now)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("expected random xor key to change the encoded id")
	}
}

const httpMethodPost = "POST"
