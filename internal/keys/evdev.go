package keys

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"time"

	"github.com/grafov/evdev"
)

// Listen opens every evdev keyboard that has a CapsLock key and feeds
// events into m until ctx is cancelled. State-machine events are sent on
// out (may be nil to discard). Passive observe: keys still work normally.
func Listen(ctx context.Context, m *Machine, out chan<- Event, debug bool) error {
	devices, err := keyboards()
	if err != nil {
		return err
	}
	if len(devices) == 0 {
		return fmt.Errorf("no keyboards with capslock found in /dev/input")
	}
	var open []*evdev.InputDevice
	defer func() {
		for _, f := range open {
			f.File.Close()
		}
	}()
	for _, d := range devices {
		f, err := evdev.Open(fmt.Sprintf("/dev/input/%s", d))
		if err != nil {
			log.Printf("keys: open %s: %v (skipping, need input group?)", d, err)
			continue
		}
		open = append(open, f)
		go readDevice(ctx, f, m, out, debug)
	}
	<-ctx.Done()
	return nil
}

func keyboards() ([]string, error) {
	matches, err := filepath.Glob("/dev/input/event*")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, path := range matches {
		f, err := evdev.Open(path)
		if err != nil {
			continue
		}
		if _, ok := f.Capabilities["EV_KEY"][keyCapsLock]; ok {
			out = append(out, filepath.Base(path))
		}
		f.File.Close()
	}
	sort.Strings(out)
	return out, nil
}

func readDevice(ctx context.Context, f *evdev.InputDevice, m *Machine, out chan<- Event, debug bool) {
	for {
		evs, err := f.Read()
		if err != nil {
			return
		}
		for _, e := range evs {
			if e.Type != 1 { // EV_KEY
				continue
			}
			ts := time.Duration(e.Time.Sec)*time.Second + time.Duration(e.Time.Usec)*time.Microsecond
			if debug {
				log.Printf("key code=%d value=%d", e.Code, e.Value)
			}
			for _, ev := range m.Feed(int(e.Code), int(e.Value), ts) {
				if ev == EvNone {
					continue
				}
				if debug {
					log.Printf("EVENT %d", ev)
				}
				if out != nil {
					select {
					case out <- ev:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}
}
