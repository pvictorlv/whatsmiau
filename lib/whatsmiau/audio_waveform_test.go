package whatsmiau

import (
	"math"
	"testing"
)

// pcmRamp builds mono PCM whose amplitude grows linearly from silence to full scale, so
// the resulting waveform must be monotonically increasing.
func pcmRamp(total int) []int16 {
	samples := make([]int16, total)
	for i := range samples {
		amplitude := float64(i) / float64(total) * 32767.0
		// alternate the sign so each bar has a real RMS instead of a DC offset
		if i%2 == 0 {
			samples[i] = int16(amplitude)
		} else {
			samples[i] = int16(-amplitude)
		}
	}
	return samples
}

func TestBuildWaveformStaysWithinWhatsAppRange(t *testing.T) {
	waveform := buildWaveform(pcmRamp(48000*3), 64)

	if len(waveform) != 64 {
		t.Fatalf("expected 64 bars, got %d", len(waveform))
	}
	for i, v := range waveform {
		if float64(v) > waveformMaxAmplitude {
			t.Fatalf("bar %d is %d, above the %v the client accepts", i, v, waveformMaxAmplitude)
		}
	}
}

func TestBuildWaveformFollowsAmplitude(t *testing.T) {
	waveform := buildWaveform(pcmRamp(48000*3), 64)

	if waveform[0] >= waveform[len(waveform)-1] {
		t.Fatalf("expected a rising waveform, got first=%d last=%d", waveform[0], waveform[len(waveform)-1])
	}
	// A ramp normalised against the 98th percentile must reach the top of the range.
	if waveform[len(waveform)-1] != byte(waveformMaxAmplitude) {
		t.Fatalf("expected the loudest bar to peak at %v, got %d", waveformMaxAmplitude, waveform[len(waveform)-1])
	}
}

func TestBuildWaveformOnSilence(t *testing.T) {
	waveform := buildWaveform(make([]int16, 48000), 64)

	if len(waveform) != 64 {
		t.Fatalf("expected 64 bars, got %d", len(waveform))
	}
	for i, v := range waveform {
		if v != 0 {
			t.Fatalf("expected silence to produce empty bars, bar %d is %d", i, v)
		}
	}
}

func TestAudioSecondsNeverReportsZero(t *testing.T) {
	tests := []struct {
		duration float64
		want     uint32
	}{
		{duration: 0, want: 1},
		{duration: 0.4, want: 1},
		{duration: 1, want: 1},
		{duration: 1.4, want: 1},
		{duration: 1.6, want: 2},
		{duration: 12.5, want: 13},
	}

	for _, tt := range tests {
		if got := audioSeconds(tt.duration); got != tt.want {
			t.Fatalf("audioSeconds(%v) = %d, want %d", tt.duration, got, tt.want)
		}
	}
}

func TestRmsByBarsHandlesShortAudio(t *testing.T) {
	// Fewer samples than bars: every bar must still be emitted so the waveform keeps the
	// fixed length the client expects.
	values := rmsByBars(pcmRamp(10), 64)

	if len(values) != 64 {
		t.Fatalf("expected 64 values, got %d", len(values))
	}
	for i, v := range values {
		if math.IsNaN(v) || v < 0 {
			t.Fatalf("value %d is invalid: %v", i, v)
		}
	}
}
