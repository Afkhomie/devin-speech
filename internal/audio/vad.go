package audio

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Vad listens on the default source via parec raw PCM and emits complete
// utterance wav files (16kHz mono s16le) on the returned channel.
// Endpointing: speech = RMS above threshold for >= speechMs; endpoint after
// silenceMs of quiet following speech. maxDur caps an utterance.
type Vad struct {
	Threshold float64 // RMS speech threshold (0-32768 scale)
	SpeechMs  int
	SilenceMs int
	MaxDurMs  int
	preRollMs int
}

func DefaultVad() *Vad {
	return &Vad{Threshold: 800, SpeechMs: 250, SilenceMs: 900, MaxDurMs: 30000, preRollMs: 300}
}

// Listen streams the audio source and sends wav paths on out.
// source "" = system default via parec; "..." containing ':' = direct ALSA
// via arecord raw PCM. Blocks until ctx done.
func (v *Vad) Listen(ctx context.Context, source string, out chan<- string) error {
	var cmd *exec.Cmd
	if strings.Contains(source, ":") {
		cmd = exec.CommandContext(ctx, "arecord",
			"-D", source, "-f", "S16_LE", "-r", "16000", "-c", "1")
	} else {
		args := []string{"--format=s16le", "--rate=16000", "--channels=1"}
		if source != "" {
			args = append([]string{"--device=" + source}, args...)
		}
		cmd = exec.CommandContext(ctx, "parec", args...)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("recorder: %w", err)
	}
	defer cmd.Process.Kill()

	const frameSamples = 320 // 20ms @16kHz
	frameBytes := frameSamples * 2
	buf := make([]byte, frameBytes)
	var utter []byte
	preRoll := newRingBuffer(max(1, v.preRollMs/20) * frameBytes)
	inSpeech := false
	speechRun := 0
	silenceRun := 0
	frames := 0

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		n, err := readFull(stdout, buf)
		if err != nil {
			return err
		}
		if n < frameBytes {
			continue
		}
		frames++
		rms := pcmRMS(buf)
		loud := rms > v.Threshold

		if !inSpeech {
			preRoll.push(buf)
			if loud {
				speechRun++
				if speechRun*20 >= v.SpeechMs {
					inSpeech = true
					silenceRun = 0
					utter = preRoll.drain()
				}
			} else {
				speechRun = 0
			}
			continue
		}

		// in speech
		utter = append(utter, buf...)
		if loud {
			silenceRun = 0
		} else {
			silenceRun += 20
			if silenceRun >= v.SilenceMs {
				// endpoint
				path, err := writeWav(utter)
				if err == nil {
					select {
					case out <- path:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				utter = nil
				inSpeech = false
				speechRun = 0
				silenceRun = 0
				preRoll.reset()
				continue
			}
		}
		if frames*20 >= v.MaxDurMs {
			path, err := writeWav(utter)
			if err == nil {
				select {
				case out <- path:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			utter = nil
			inSpeech = false
			speechRun = 0
			silenceRun = 0
			preRoll.reset()
			frames = 0
		}
	}
}

func pcmRMS(b []byte) float64 {
	var sum float64
	n := len(b) / 2
	for i := 0; i < n; i++ {
		v := int16(binary.LittleEndian.Uint16(b[i*2:]))
		sum += float64(v) * float64(v)
	}
	return math.Sqrt(sum / float64(n))
}

func readFull(r interface{ Read([]byte) (int, error) }, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
		if n == 0 {
			break
		}
	}
	return total, nil
}

func writeWav(pcm []byte) (string, error) {
	dir := os.TempDir() + "/devin-speech"
	os.MkdirAll(dir, 0o755)
	path := fmt.Sprintf("%s/vad-%d.wav", dir, time.Now().UnixNano())
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	dataLen := uint32(len(pcm))
	hdr := make([]byte, 44)
	copy(hdr[0:], "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:], 36+dataLen)
	copy(hdr[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(hdr[16:], 16)
	binary.LittleEndian.PutUint16(hdr[20:], 1)     // PCM
	binary.LittleEndian.PutUint16(hdr[22:], 1)     // mono
	binary.LittleEndian.PutUint32(hdr[24:], 16000) // rate
	binary.LittleEndian.PutUint32(hdr[28:], 32000) // byte rate
	binary.LittleEndian.PutUint16(hdr[32:], 2)     // block align
	binary.LittleEndian.PutUint16(hdr[34:], 16)    // bits
	copy(hdr[36:], "data")
	binary.LittleEndian.PutUint32(hdr[40:], dataLen)
	if _, err := f.Write(hdr); err != nil {
		return "", err
	}
	_, err = f.Write(pcm)
	return path, err
}

func newRingBuffer(n int) *ringBuffer { return &ringBuffer{buf: make([]byte, n)} }

type ringBuffer struct {
	buf []byte
	pos int
	len int
}

func (r *ringBuffer) push(b []byte) {
	for _, x := range b {
		r.buf[r.pos] = x
		r.pos = (r.pos + 1) % len(r.buf)
		if r.len < len(r.buf) {
			r.len++
		}
	}
}

func (r *ringBuffer) drain() []byte {
	out := make([]byte, r.len)
	start := (r.pos - r.len + len(r.buf)) % len(r.buf)
	for i := 0; i < r.len; i++ {
		out[i] = r.buf[(start+i)%len(r.buf)]
	}
	r.len = 0
	return out
}

func (r *ringBuffer) reset() { r.len = 0; r.pos = 0 }
