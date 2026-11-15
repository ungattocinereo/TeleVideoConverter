package media

import "testing"

func limits() Limits {
	return Limits{SendLimitBytes: 50 << 20, TargetBytes: 48 << 20, MinVideoKbps: 250, AudioKbps: 64, Policy: "compress_then_split"}
}

func TestShortVideoUsesSinglePartWithHighBitrate(t *testing.T) {
	l := limits()
	if n := l.PartCount(60, true); n != 1 {
		t.Fatalf("parts = %d, want 1", n)
	}
	v, a := l.Bitrates(60, true)
	if v <= 1000 || a != 64 {
		t.Fatalf("bitrates = %d/%d", v, a)
	}
}

func TestLongVideoIsSplitToKeepMinimumBitrate(t *testing.T) {
	l := limits()
	n := l.PartCount(7200, true)
	if n <= 1 {
		t.Fatalf("parts = %d, want > 1", n)
	}
	v, a := l.Bitrates(7200/float64(n), true)
	if v < l.MinVideoKbps || a != 64 {
		t.Fatalf("bitrates = %d/%d", v, a)
	}
}

func TestVideoWithoutAudioGetsVideoOnlyBudget(t *testing.T) {
	v, a := limits().Bitrates(300, false)
	if v <= 250 || a != 0 {
		t.Fatalf("bitrates = %d/%d", v, a)
	}
}

func TestResolutionLadder(t *testing.T) {
	for kbps, want := range map[int]int{1200: 720, 800: 540, 500: 480, 300: 360} {
		if got := MaxSide(kbps); got != want {
			t.Errorf("MaxSide(%d) = %d, want %d", kbps, got, want)
		}
	}
}

func TestCopyRule(t *testing.T) {
	cases := []struct {
		p    Probe
		copy bool
	}{
		{Probe{HasVideo: true, VideoCodec: "h264", Profile: "High", PixFmt: "yuv420p", Width: 720, Height: 1280}, true},
		{Probe{HasVideo: true, VideoCodec: "h264", Profile: "High 10", PixFmt: "yuv420p10le", Width: 720, Height: 1280}, false},
		{Probe{HasVideo: true, VideoCodec: "hevc", Profile: "Main", PixFmt: "yuv420p", Width: 720, Height: 1280}, false},
		{Probe{HasVideo: true, VideoCodec: "h264", Profile: "High", PixFmt: "yuv420p", Width: 721, Height: 1280}, false},
	}
	for _, c := range cases {
		if got := c.p.videoPlaysEverywhere(); got != c.copy {
			t.Errorf("%+v: copy = %v, want %v", c.p, got, c.copy)
		}
	}
}

func TestStandardFPSAndEncodePlan(t *testing.T) {
	for in, want := range map[float64]int{28.79: 30, 30.006: 30, 23.976: 24, 25: 25, 59.94: 60, 0: 30} {
		if got := StandardFPS(in); got != want {
			t.Errorf("StandardFPS(%v) = %d, want %d", in, got, want)
		}
	}
	h264 := Probe{HasVideo: true, VideoCodec: "h264", PixFmt: "yuv420p", Width: 720, Height: 720}
	if !h264.WillEncode(400<<10, 48<<20) {
		t.Error("small H.264 must be encoded for Telegram")
	}
	if h264.WillEncode(300<<20, 48<<20) {
		t.Error("large H.264 should only be remuxed")
	}
	hevc := Probe{HasVideo: true, VideoCodec: "hevc", PixFmt: "yuv420p", Width: 720, Height: 720}
	if !hevc.WillEncode(300<<20, 48<<20) {
		t.Error("HEVC must always be encoded")
	}
	if parseRate("197100000/6568643") < 30 || parseRate("30/1") != 30 {
		t.Error("parseRate")
	}
}
