package main

import (
	"context"
	"edgegate/internal/proxy"
	"flag"
	"os/signal"
	"syscall"
)

func main() {

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	configPath := flag.String("conf", "./configs/edgegate.yaml", "config path")
	flag.Parse()
	egproxy.StartEngine(rootCtx, *configPath)
}
