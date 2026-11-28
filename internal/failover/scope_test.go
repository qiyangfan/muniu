package failover

import "testing"

func TestEvidenceScope_信号表(t *testing.T) {
	if !RetrySameProvider(ClassRateLimited) || !RetrySameProvider(ClassAuthFailed) {
		t.Fatal("429/401 应允许同 provider 换 key")
	}
	if RetrySameProvider(ClassUpstream5xx) || RetrySameProvider(ClassUnknown) {
		t.Fatal("渠道/候选级错误不得同 provider 换 key")
	}
	for _, c := range []ErrorClass{ClassDialFail, ClassTimeoutFirst, ClassUpstream5xx, ClassMidStreamFail} {
		if !ChannelSignal(c) {
			t.Fatalf("%s 应计入渠道证据", c)
		}
	}
	for _, c := range []ErrorClass{ClassRateLimited, ClassAuthFailed, ClassBadRequest, ClassUnknown} {
		if ChannelSignal(c) {
			t.Fatalf("%s 不得计入渠道证据", c)
		}
	}
}
