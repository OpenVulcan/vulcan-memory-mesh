package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/openvulcan/vmm/internal/app"
	"github.com/openvulcan/vmm/internal/config"
)

func main() {
	cfgPath := flag.String("config", "configs/saas.json", "path to config file")
	flag.Parse()
	cfg, err := config.Load(*cfgPath, config.DefaultSaaS())
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}
	application, err := app.NewSaaS(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "build app: %v\n", err)
		os.Exit(1)
	}
	if err := application.Run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "run app: %v\n", err)
		os.Exit(1)
	}
}
