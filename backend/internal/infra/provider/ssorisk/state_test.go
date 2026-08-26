package ssorisk

import (
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestParseAndClassifyGrokHomepageBotFlag(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		html    string
		source  int
		verdict string
		denied  bool
	}{
		{
			name:    "clean zero",
			html:    `{"botFlagSource":0,"botFlagDetails":"event=$login,policy=allow,risk=0.1"}`,
			source:  0,
			verdict: VerdictClean,
		},
		{
			name:    "escaped rsc payload",
			html:    `self.__next_f.push([1,"{\"botFlagSource\":0,\"botFlagDetails\":\"event=$login,policy=allow,risk=0.1\"}"])`,
			source:  0,
			verdict: VerdictClean,
		},
		{
			name:    "account robot",
			html:    `{"botFlagSource":1,"botFlagDetails":"event=$registration,policy=deny,risk=0.9"}`,
			source:  1,
			verdict: VerdictFlagged,
			denied:  true,
		},
		{
			name:    "ip soft mark",
			html:    `botFlagSource":2,"botFlagDetails":"event=$login,policy=allow,risk=0.4,eapi_ip_bot_farm=1`,
			source:  2,
			verdict: VerdictFlagged,
		},
		{
			name:    "deny without numeric source",
			html:    `botFlagSource":null,"botFlagDetails":"event=$login,policy=deny,risk=0.8"`,
			source:  1,
			verdict: VerdictFlagged,
			denied:  true,
		},
		{
			name:    "missing fields",
			html:    `<html><body>no flags</body></html>`,
			source:  0,
			verdict: VerdictUnknown,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := Parse(test.html)
			if FlagSource(state) != test.source {
				t.Fatalf("source = %d, want %d (state=%#v)", FlagSource(state), test.source, state)
			}
			if Classify(state) != test.verdict {
				t.Fatalf("verdict = %s, want %s", Classify(state), test.verdict)
			}
			if state.Denied != test.denied {
				t.Fatalf("denied = %t, want %t", state.Denied, test.denied)
			}
		})
	}
}

func TestParseUserMessageGetUserProto(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		message []byte
		source  int
		verdict string
		denied  bool
		found   bool
	}{
		{
			name:    "omitted flags are clean",
			message: encodeTestUser(0, "", false),
			source:  0,
			verdict: VerdictClean,
			found:   true,
		},
		{
			name:    "castle deny",
			message: encodeTestUser(1, "event=$registration,policy=deny,risk=0.9", true),
			source:  1,
			verdict: VerdictFlagged,
			denied:  true,
			found:   true,
		},
		{
			name:    "bot monitor",
			message: encodeTestUser(2, "event=$login,policy=allow,risk=0.4,eapi_ip_bot_farm=1", true),
			source:  2,
			verdict: VerdictFlagged,
			found:   true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, err := ParseUserMessage(test.message)
			if err != nil {
				t.Fatal(err)
			}
			if state.Found != test.found {
				t.Fatalf("found = %t, want %t", state.Found, test.found)
			}
			if FlagSource(state) != test.source {
				t.Fatalf("source = %d, want %d (state=%#v)", FlagSource(state), test.source, state)
			}
			if Classify(state) != test.verdict {
				t.Fatalf("verdict = %s, want %s", Classify(state), test.verdict)
			}
			if state.Denied != test.denied {
				t.Fatalf("denied = %t, want %t", state.Denied, test.denied)
			}
		})
	}
}

func encodeTestUser(source int, details string, includeSource bool) []byte {
	message := protowire.AppendString(protowire.AppendTag(nil, 1, protowire.BytesType), "user-1")
	if includeSource {
		message = protowire.AppendVarint(protowire.AppendTag(message, 45, protowire.VarintType), uint64(source))
	}
	if details != "" {
		message = protowire.AppendString(protowire.AppendTag(message, 46, protowire.BytesType), details)
	}
	return message
}
