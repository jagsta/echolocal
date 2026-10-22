package echod

import (
	"context"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/ygelfand/libcountertop/pkg/audio/upsample"

	"github.com/ygelfand/echolocal/internal/hardware/mic"
	"github.com/ygelfand/echolocal/internal/hardware/speaker"
	"github.com/ygelfand/echolocal/internal/lib/audio"
)

const maxLag = 800

func newEchoCmd() *cobra.Command {
	var (
		secs  float64
		level float64
		save  string
		white bool
	)

	c := &cobra.Command{
		Use:   "echo",
		Short: "Measure how much of the speaker each microphone hears",
		Long: "Plays a sweep and reports, per microphone, how much of what came back is the\n" +
			"playback loopback on ch7/ch8 rather than the room.\n\n" +
			"This is the measurement echo cancellation is designed against: the lag says how\n" +
			"far the filter has to reach, and the one-tap residual is a floor on what removing\n" +
			"the echo can buy — a real filter does better, never worse.\n\n" +
			"echod holds both devices, so stop the service first and start it after:\n" +
			"  setprop ctl.stop ledcontroller\n" +
			"  setprop ctl.start ledcontroller\n" +
			"On an Echo Show the service is echod.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()

			source, err := mic.Acquire()
			if err != nil {
				return err
			}
			defer func() { _ = source.Close() }()

			p, err := speaker.Acquire()
			if err != nil {
				return err
			}
			defer func() { _ = p.Close() }()

			ctx, stop := context.WithCancel(cmd.Context())
			defer stop()
			go func() { _ = source.Run(ctx) }()
			go func() { _ = p.Run(ctx) }()

			time.Sleep(1500 * time.Millisecond)

			raw, unlisten := source.ListenRaw()
			defer unlisten()

			signal, what := upsample.Sweep(), "a sweep"
			if white {
				signal, what = noise(int(secs*1000)), "white noise"
			}
			fmt.Fprintf(out, "playing %s for %.1fs at level %.2f\n", what, secs, level)
			p.PlayVoice(amplify(signal, level))

			frames, err := collect(ctx, raw, secs)
			if err != nil {
				return err
			}
			report(out, frames)

			if save != "" {
				if err := os.WriteFile(save, frames, 0o644); err != nil {
					return err
				}
				fmt.Fprintf(
					out,
					"\nwrote %d bytes of interleaved capture to %s\n",
					len(frames),
					save,
				)
			}
			return nil
		},
	}

	c.Flags().Float64Var(&secs, "secs", 4, "how long to capture")
	c.Flags().
		Float64Var(&level, "level", 0.25, "scales the sweep, 1 being the half-scale one the speaker uses")
	c.Flags().
		StringVar(&save, "save", "", "write the raw interleaved capture here, for sizing a filter off the device")
	c.Flags().
		BoolVar(&white, "noise", false, "play white noise for the whole capture instead of the 1.5s sweep")
	return c
}

func noise(ms int) []int16 {
	frames := speaker.VoiceRate * ms / 1000
	out := make([]int16, frames)

	r := rand.New(rand.NewPCG(1, 2))
	for i := range out {
		env := math.Min(1, math.Min(float64(i), float64(frames-i))/float64(speaker.VoiceRate/50))
		out[i] = int16(0.5 * env * math.MaxInt16 * (r.Float64()*2 - 1))
	}
	return out
}

func collect(ctx context.Context, raw <-chan []byte, secs float64) ([]byte, error) {
	var all []byte
	deadline := time.After(time.Duration(secs * float64(time.Second)))

	for {
		select {
		case <-ctx.Done():
			return all, ctx.Err()
		case <-deadline:
			return all, nil
		case frame, ok := <-raw:
			if !ok {
				return all, nil
			}
			all = append(all, frame...)
		}
	}
}

func report(w io.Writer, frames []byte) {
	mics := mic.Decode(frames)
	refs := mic.Reference(frames)
	if len(mics) == 0 || len(refs) == 0 || len(mics[0]) == 0 {
		fmt.Fprintln(w, "nothing captured")
		return
	}

	// The speaker is mono into a single driver.
	ref := make([]float64, len(refs[0]))
	for i := range ref {
		for c := range refs {
			ref[i] += float64(refs[c][i])
		}
	}

	fmt.Fprintf(w, "\ncaptured %d samples, reference %.1f dBFS\n", len(ref), dbfs(ref))
	if dbfs(ref) < -90 {
		fmt.Fprintln(w, "the loopback is silent, so the lag and erle columns mean nothing")
	}

	fmt.Fprintf(
		w,
		"\n%4s %10s %8s %8s %10s %8s\n",
		"mic",
		"level",
		"lag",
		"lag ms",
		"residual",
		"erle",
	)
	for c, m := range mics {
		got := make([]float64, len(m))
		for i, v := range m {
			got[i] = float64(v)
		}

		lag, _ := audio.BestLag(ref, got, maxLag)
		res := residual(got, ref, lag)

		fmt.Fprintf(w, "%4d %10.1f %8d %8.2f %10.1f %8.1f\n",
			c, dbfs(got), lag, float64(lag)*1000/float64(mic.Rate), dbfs(res), dbfs(got)-dbfs(res))
	}

	fmt.Fprintln(w, "\nerle is what one delayed, scaled copy removes. A filter with a tail does")
	fmt.Fprintln(w, "better; this is the floor, and it says the echo is there to be found.")
}

func residual(got, ref []float64, lag int) []float64 {
	a, b := align(got, ref, lag)

	var num, den float64
	for i := range a {
		num += a[i] * b[i]
		den += b[i] * b[i]
	}
	if den == 0 {
		return a
	}
	gain := num / den

	out := make([]float64, len(a))
	for i := range a {
		out[i] = a[i] - gain*b[i]
	}
	return out
}

func align(got, ref []float64, lag int) (a, b []float64) {
	switch {
	case lag < 0:
		return got[:len(got)+lag], ref[-lag:]
	case lag > 0:
		return got[lag:], ref[:len(ref)-lag]
	}
	n := min(len(got), len(ref))
	return got[:n], ref[:n]
}

// audio.DBFS is against a 24-bit full scale.
func dbfs(x []float64) float64 {
	if len(x) == 0 {
		return math.Inf(-1)
	}
	var sum float64
	for _, v := range x {
		sum += v * v
	}
	return audio.DBFS(math.Sqrt(sum/float64(len(x))) * 256)
}
