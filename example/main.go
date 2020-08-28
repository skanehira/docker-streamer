package main

import (
	"context"
	"log"
	"os"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	streamer "github.com/skanehira/docker-streamer"
)

func main() {
	os.Setenv("DOCKER_API_VERSION", "1.40")
	cli, err := client.NewEnvClient()
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()

	response, err := cli.ContainerExecCreate(ctx, "golang", types.ExecConfig{
		Tty:          true,
		AttachStdin:  true,
		AttachStderr: true,
		AttachStdout: true,
		Cmd:          []string{"bash"},
	})

	if err != nil {
		log.Fatal(err)
	}

	execID := response.ID
	if execID == "" {
		log.Fatalf("empty exec id")
	}

	s := streamer.New()

	resp, err := cli.ContainerExecAttach(ctx, execID, types.ExecStartCheck{Tty: true})
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Close()

	f := func(ctx context.Context, id string, options types.ResizeOptions) error {
		return cli.ContainerExecResize(ctx, id, options)
	}

	if err := s.Stream(ctx, execID, resp, streamer.ResizeContainer(f)); err != nil {
		log.Fatal(err)
	}
}
