package ssorisk

import "testing"

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
