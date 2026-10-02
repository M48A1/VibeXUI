package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
	"vibexui/internal/agent"
)

func main() {
	o := agent.Options{}
	registerOnly := flag.Bool("register-only", false, "仅注册并保存凭据，不启动 Xray")
	flag.StringVar(&o.Panel, "panel", "", "主面板 HTTPS 地址")
	flag.StringVar(&o.ID, "server-id", "", "服务器 ID")
	flag.StringVar(&o.RegistrationToken, "registration-token", "", "一次性注册令牌（也可使用 VIBEXUI_REGISTRATION_TOKEN）")
	flag.StringVar(&o.Directory, "data-dir", "data/agent", "凭据和配置目录")
	flag.StringVar(&o.Xray, "xray", "xray", "Xray-core 二进制路径")
	flag.DurationVar(&o.Interval, "interval", 10*time.Second, "上报间隔")
	flag.Parse()
	if o.RegistrationToken == "" {
		o.RegistrationToken = os.Getenv("VIBEXUI_REGISTRATION_TOKEN")
	}
	a, err := agent.New(o)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *registerOnly {
		err = a.Register(ctx)
	} else {
		err = a.Run(ctx)
	}
	if err != nil {
		log.Fatal(err)
	}
}
