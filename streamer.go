package streamer

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"sync"
	"syscall"
	"time"

	"os/signal"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/pkg/term"
)

type ResizeContainer func(ctx context.Context, id string, options types.ResizeOptions) error

var (
	ErrEmptyExecID   = errors.New("emtpy exec id")
	ErrTtySizeIsZero = errors.New("tty size is 0")
)

type Streamer struct {
	in    *In
	out   *Out
	err   io.Writer
	isTty bool
}

func New() *Streamer {
	return &Streamer{
		in:  NewIn(os.Stdin),
		out: NewOut(os.Stdout),
		err: os.Stderr,
	}
}

func (s *Streamer) Stream(ctx context.Context, id string, resp types.HijackedResponse, resize ResizeContainer) (err error) {
	if id == "" {
		return ErrEmptyExecID
	}

	errCh := make(chan error, 1)

	go func() {
		defer close(errCh)
		errCh <- s.stream(ctx, resp)
	}()

	if s.in.IsTerminal {
		s.monitorTtySize(ctx, resize, id)
	}

	if err := <-errCh; err != nil {
		log.Printf("stream error: %s", err)
		return err
	}

	return nil
}

func (s *Streamer) stream(ctx context.Context, resp types.HijackedResponse) error {
	// set raw mode
	restore, err := s.SetRawTerminal()
	if err != nil {
		return err
	}
	defer restore()

	// start stdin/stdout stream
	outDone := s.streamOut(restore, resp)
	inDone := s.streamIn(restore, resp)

	select {
	case err := <-outDone:
		return err
	case <-inDone:
		select {
		case err := <-outDone:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Streamer) streamIn(restore func(), resp types.HijackedResponse) <-chan struct{} {
	done := make(chan struct{})

	go func() {
		_, err := io.Copy(resp.Conn, s.in)
		restore()

		if _, ok := err.(term.EscapeError); ok {
			// TODO handling esape
			return
		}

		if err != nil {
			log.Printf("in stream error: %s", err)
		}

		if err := resp.CloseWrite(); err != nil {
			log.Printf("close response error: %s", err)
		}

		close(done)
	}()

	return done
}

func (s *Streamer) streamOut(restore func(), resp types.HijackedResponse) <-chan error {
	done := make(chan error, 1)

	go func() {
		_, err := io.Copy(s.out, resp.Reader)
		restore()

		if err != nil {
			log.Printf("output stream error: %s", err)
		}

		done <- err
	}()

	return done
}

func (s *Streamer) SetRawTerminal() (func(), error) {
	if err := s.in.SetRawTerminal(); err != nil {
		return nil, err
	}

	var once sync.Once
	restore := func() {
		once.Do(func() {
			if err := s.in.RestoreTerminal(); err != nil {
				log.Printf("failed to restore terminal: %s\n", err)
			}
		})
	}

	return restore, nil
}

func (s *Streamer) resizeTty(ctx context.Context, resize ResizeContainer, id string) error {
	h, w := s.out.GetTtySize()
	if h == 0 && w == 0 {
		return ErrTtySizeIsZero
	}

	options := types.ResizeOptions{
		Height: h,
		Width:  w,
	}

	return resize(ctx, id, options)
}

func (s *Streamer) initTtySize(ctx context.Context, resize ResizeContainer, id string) {
	if err := s.resizeTty(ctx, resize, id); err != nil {
		go func() {
			log.Printf("failed to resize tty: %s\n", err)
			for retry := 0; retry < 5; retry++ {
				time.Sleep(10 * time.Millisecond)
				if err = s.resizeTty(ctx, resize, id); err == nil {
					break
				}
			}
			if err != nil {
				log.Println("failed to resize tty, using default size")
			}
		}()
	}
}

func (s *Streamer) monitorTtySize(ctx context.Context, resize ResizeContainer, id string) {
	s.initTtySize(ctx, resize, id)
	sigchan := make(chan os.Signal, 1)
	signal.Notify(sigchan, syscall.SIGWINCH)
	go func() {
		for range sigchan {
			s.resizeTty(ctx, resize, id)
		}
	}()
}
