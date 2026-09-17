package constant

import "testing"

func TestPrivateChannelTypeIDsDoNotOverlapUpstream(t *testing.T) {
	upstream := []int{ChannelTypeDoubaoVideo, ChannelTypeSub2API, ChannelTypeNewAPI, ChannelTypeTaskPlugin, ChannelTypeVLLM, ChannelTypeSGLang}
	// OpenAISeedance=1000 is a previously shipped private ID and must remain
	// stable. New private IDs start at 1002 because 1001 is also occupied by
	// an earlier fork release.
	private := []int{ChannelTypeHappyHorse, ChannelTypeSeedanceDomestic, ChannelTypeMobileCloudSeedance}
	for _, id := range private {
		if id != ChannelTypeHappyHorse && id < ChannelTypePrivateBase {
			t.Fatalf("private channel ID %d is below private base %d", id, ChannelTypePrivateBase)
		}
		for _, upstreamID := range upstream {
			if id == upstreamID {
				t.Fatalf("private channel ID %d overlaps upstream ID", id)
			}
		}
	}
}

func TestGetChannelBaseURLPrivateTypeIsSafe(t *testing.T) {
	if got := GetChannelBaseURL(ChannelTypeSeedanceDomestic); got != "https://api.laomandi.com" {
		t.Fatalf("unexpected Seedance base URL: %q", got)
	}
	if got := GetChannelBaseURL(999999); got != "" {
		t.Fatalf("unknown channel type should have empty base URL, got %q", got)
	}
}
